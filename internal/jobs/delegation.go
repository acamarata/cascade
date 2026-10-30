package jobs

// Purpose: the delegation record store (contract:delegation-record). Each
//
//	row holds one delegation's controller-signed scope envelope and its
//	parent's. Rows are insert-only: BindDelegationMarker and
//	BindDelegationSpawn each set their columns once, FinishDelegation
//	moves live -> terminal once, and the triggers in delegation_schema.go
//	refuse every other UPDATE and every DELETE at the database.
//
// Derived, never trusted: InsertDelegation reads only the envelope in
// r.Signed for EnvelopeDigest and ParentDigest, and only the stored parent
// row (or, for a first hop, the root envelope in r.ParentSigned) for
// Depth, OriginPrincipal and ParentSigned. A caller-supplied Depth or
// OriginPrincipal is ignored. The depth and fan-out checks run inside the
// insert's own transaction and statement (m-6).
//
// Caps: DelegationMaxDepth and DelegationMaxLiveChildren live here, not in
// internal/delegation, because internal/delegation imports this package;
// delegation.MaxDepth and delegation.MaxLiveChildren alias them so the limits
// have one source of truth.
//
// SPORT: jobs/delegation-record/ADD (P1-AGT-11).

import (
	"bytes"
	"context"
	"database/sql"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/envelope"
)

// The delegation caps: the deepest record depth (a root envelope is depth
// 0) and the most live children one parent envelope may hold.
const (
	DelegationMaxDepth        = 3
	DelegationMaxLiveChildren = 8
)

// DelegationState is a record's lifecycle state.
type DelegationState string

// The record states: live, then exactly one terminal state.
const (
	DelegationLive      DelegationState = "live"
	DelegationSucceeded DelegationState = "succeeded"
	DelegationFailed    DelegationState = "failed"
	DelegationCancelled DelegationState = "cancelled"
	DelegationRefused   DelegationState = "refused"
)

// terminal reports whether st is one of the four terminal states.
func (st DelegationState) terminal() bool {
	switch st {
	case DelegationSucceeded, DelegationFailed, DelegationCancelled, DelegationRefused:
		return true
	case DelegationLive:
		return false
	}
	return false
}

// DelegationRecord is one stored delegation.
type DelegationRecord struct {
	ID              string // = child envelope ID
	EnvelopeDigest  string
	Signed          []byte // envelope.Signed JSON
	ParentDigest    string
	ParentSigned    []byte
	OriginPrincipal string // principal ID or "owner-cli"
	OriginSession   string
	AgentPrincipal  string
	Depth           int
	JobID           string
	Mutation        bool
	Lane            string
	LeaseID         string
	AgentJobID      string
	LeaseMarker     string // set-once before spawn (M-4)
	State           DelegationState
	Reason          string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// InsertDelegation stores r as a live record. r.State must be live, the
// bindings and Reason empty, r.ID the envelope's ID and r.AgentPrincipal
// its Principal. A duplicate ID, envelope or agent principal is
// KindConflict; a depth over DelegationMaxDepth or a parent already
// holding DelegationMaxLiveChildren live children is KindPermissionDenied.
// CreatedAt is the caller's injected-clock time; UpdatedAt is set to it.
func (s *Store) InsertDelegation(ctx context.Context, r DelegationRecord) error {
	env, digest, err := checkInsert(r)
	if err != nil {
		return err
	}
	row := r
	row.EnvelopeDigest, row.ParentDigest, row.UpdatedAt = digest, env.ParentDigest, r.CreatedAt
	return s.withTx(ctx, func(tx *sql.Tx) error {
		if err := deriveParentTx(ctx, tx, &row, env, r.ParentSigned); err != nil {
			return err
		}
		return insertDelegationTx(ctx, tx, row)
	})
}

// checkInsert validates the caller's fields and decodes the envelope.
func checkInsert(r DelegationRecord) (envelope.Envelope, string, error) {
	switch {
	case r.State != DelegationLive:
		return envelope.Envelope{}, "", invalidDelegation("a delegation is inserted live, not %q", string(r.State))
	case r.LeaseID != "" || r.AgentJobID != "" || r.LeaseMarker != "" || r.Reason != "":
		return envelope.Envelope{}, "", invalidDelegation("bindings and reason are set after insert")
	case r.CreatedAt.IsZero():
		return envelope.Envelope{}, "", invalidDelegation("CreatedAt is required")
	}
	signed, err := envelope.Decode(r.Signed)
	if err != nil {
		return envelope.Envelope{}, "", err
	}
	env := signed.Env
	if env.ID == "" || env.ID != r.ID {
		return envelope.Envelope{}, "", invalidDelegation("record ID %q is not the envelope ID %q", r.ID, env.ID)
	}
	if r.AgentPrincipal == "" || r.AgentPrincipal != env.Principal {
		return envelope.Envelope{}, "", invalidDelegation("AgentPrincipal %q is not the envelope principal %q", r.AgentPrincipal, env.Principal)
	}
	digest, err := envelope.Digest(env)
	if err != nil {
		return envelope.Envelope{}, "", err
	}
	return env, digest, nil
}

// deriveParentTx fills row's Depth, OriginPrincipal and ParentSigned from
// the stored parent, or from the root envelope a first hop carries.
func deriveParentTx(ctx context.Context, tx *sql.Tx, row *DelegationRecord, env envelope.Envelope, callerParent []byte) error {
	if env.ParentDigest == "" {
		if len(callerParent) != 0 {
			return invalidDelegation("a root record carries no ParentSigned")
		}
		row.Depth, row.OriginPrincipal, row.ParentSigned = 0, env.Principal, nil
		return nil
	}
	p, ok, err := scanDelegation(tx.QueryRowContext(ctx,
		`SELECT `+delegationCols+` FROM `+tableDelegation+` WHERE envelope_digest = ?`, env.ParentDigest))
	switch {
	case err != nil:
		return err
	case ok && p.State != DelegationLive:
		return cascade.Newf(cascade.KindConflict, "jobs: parent delegation %q is %s, not live", p.ID, p.State)
	case ok && len(callerParent) != 0 && !bytes.Equal(callerParent, p.Signed):
		return cascade.Newf(cascade.KindIntegrity, "jobs: ParentSigned of %q is not the stored parent's Signed", row.ID)
	case ok:
		row.Depth, row.OriginPrincipal, row.ParentSigned = p.Depth+1, p.OriginPrincipal, p.Signed
	default:
		origin, err := firstHopRoot(env, callerParent)
		if err != nil {
			return err
		}
		row.Depth, row.OriginPrincipal, row.ParentSigned = 1, origin, callerParent
	}
	if row.Depth > DelegationMaxDepth {
		return cascade.Newf(cascade.KindPermissionDenied,
			"jobs: delegation depth %d exceeds MaxDepth %d", row.Depth, DelegationMaxDepth)
	}
	return nil
}

// BindDelegationMarker sets the record's lease marker once, before spawn
// (M-4). A second call, even with the same marker, is KindConflict.
func (s *Store) BindDelegationMarker(ctx context.Context, id, marker string) error {
	if marker == "" {
		return invalidDelegation("a lease marker is required")
	}
	return s.updateLiveDelegation(ctx, id, "bind the marker of",
		func(DelegationRecord) string { return "its lease marker is already bound" },
		`UPDATE `+tableDelegation+` SET lease_marker = ? WHERE id = ? AND state = 'live' AND lease_marker IS NULL`,
		marker, id)
}

// BindDelegationSpawn sets the spawned lease and agent job once, after
// the marker. A second call, even with the same values, is KindConflict.
func (s *Store) BindDelegationSpawn(ctx context.Context, id, leaseID, agentJobID string) error {
	if leaseID == "" || agentJobID == "" {
		return invalidDelegation("a lease ID and an agent job ID are required")
	}
	why := func(r DelegationRecord) string {
		if r.LeaseID != "" {
			return "its spawn is already bound"
		}
		return "its lease marker is not bound yet (M-4)"
	}
	return s.updateLiveDelegation(ctx, id, "bind the spawn of", why,
		`UPDATE `+tableDelegation+` SET lease_id = ?, agent_job_id = ?
		 WHERE id = ? AND state = 'live' AND lease_id IS NULL AND lease_marker IS NOT NULL`,
		leaseID, agentJobID, id)
}

// FinishDelegation moves a live record to the terminal state st.
func (s *Store) FinishDelegation(ctx context.Context, id string, st DelegationState, reason string) error {
	if !st.terminal() {
		return invalidDelegation("%q is not a terminal delegation state", string(st))
	}
	return s.updateLiveDelegation(ctx, id, "finish", nil,
		`UPDATE `+tableDelegation+` SET state = ?, reason = ? WHERE id = ? AND state = 'live'`,
		string(st), reason, id)
}

// updateLiveDelegation runs one conditional UPDATE and, in the same
// transaction, types a refusal: NotFound, or Conflict naming the reason.
func (s *Store) updateLiveDelegation(ctx context.Context, id, verb string, why func(DelegationRecord) string, query string, args ...any) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, query, args...)
		if err != nil {
			return cascade.Wrap(cascade.KindUnavailable, err, "jobs: "+verb+" delegation")
		}
		if n, err := res.RowsAffected(); err == nil && n == 1 {
			return nil
		}
		r, ok, err := scanDelegation(tx.QueryRowContext(ctx,
			`SELECT `+delegationCols+` FROM `+tableDelegation+` WHERE id = ?`, id))
		switch {
		case err != nil:
			return err
		case !ok:
			return cascade.Newf(cascade.KindNotFound, "jobs: delegation %q does not exist", id)
		case r.State != DelegationLive || why == nil:
			return cascade.Newf(cascade.KindConflict, "jobs: cannot %s delegation %q: it is %s", verb, id, r.State)
		}
		return cascade.Newf(cascade.KindConflict, "jobs: cannot %s delegation %q: %s", verb, id, why(r))
	})
}

// GetDelegation reads one record by ID; a missing one is KindNotFound.
func (s *Store) GetDelegation(ctx context.Context, id string) (DelegationRecord, error) {
	r, ok, err := scanDelegation(s.db.QueryRowContext(ctx,
		`SELECT `+delegationCols+` FROM `+tableDelegation+` WHERE id = ?`, id))
	if err == nil && !ok {
		err = cascade.Newf(cascade.KindNotFound, "jobs: delegation %q does not exist", id)
	}
	return r, err
}

// DelegationByAgentPrincipal returns the one record whose AgentPrincipal
// is principalID, in any state; ok is false when there is none.
func (s *Store) DelegationByAgentPrincipal(ctx context.Context, principalID string) (DelegationRecord, bool, error) {
	if principalID == "" {
		return DelegationRecord{}, false, invalidDelegation("a principal ID is required")
	}
	return scanDelegation(s.db.QueryRowContext(ctx,
		`SELECT `+delegationCols+` FROM `+tableDelegation+` WHERE agent_principal = ?`, principalID))
}

// CountLiveChildren counts the live records whose ParentDigest is
// parentDigest.
func (s *Store) CountLiveChildren(ctx context.Context, parentDigest string) (int, error) {
	if parentDigest == "" {
		return 0, invalidDelegation("a parent digest is required")
	}
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+tableDelegation+
		` WHERE parent_digest = ? AND state = 'live'`, parentDigest).Scan(&n)
	if err != nil {
		return 0, cascade.Wrap(cascade.KindUnavailable, err, "jobs: count live delegation children")
	}
	return n, nil
}

// LiveDelegations lists every live record, oldest first, for start-up
// recovery.
func (s *Store) LiveDelegations(ctx context.Context) ([]DelegationRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+delegationCols+` FROM `+tableDelegation+
		` WHERE state = 'live' ORDER BY created_at, id`)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "jobs: list live delegations")
	}
	defer func() { _ = rows.Close() }()
	var out []DelegationRecord
	for rows.Next() {
		r, _, err := scanDelegation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "jobs: list live delegations")
	}
	return out, nil
}

// invalidDelegation builds a KindInvalidInput error with the jobs prefix.
func invalidDelegation(format string, args ...any) error {
	return cascade.Newf(cascade.KindInvalidInput, "jobs: delegation: "+format, args...)
}
