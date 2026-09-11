package evidence

// Purpose: Store is this package's persisted CRUD surface over the
// jobs_claim table (claim_store.go) and the jobs_claim_evidence table
// (evidence_store.go), split across files per the 300-line cap, following
// internal/jobs's own Store/store_job.go/store_exec.go precedent.
// PutClaim/GetClaim/ClaimsByRun/Invalidate/AddContradiction/
// AppendVerification live here.
//
// Inputs: an open *sql.DB already migrated via ApplySchema, plus an
// injected runtime.Clock for Invalidate's timestamp.
// Outputs: typed A-T7 errors; KindNotFound/KindInvalidInput/KindConflict
// per this package's errors.go sentinels.
// Constraints: a claim with zero jobs_claim_evidence rows is never
// readable as established (GetClaim enforces ErrClaimWithoutEvidence);
// data_class is write-once (ErrDataClassImmutable on a differing later
// write); Invalidate stamps invalidated_at from the injected clock and
// refuses a second call with ErrClaimInvalidated.
//
// SPORT: evidence/claim-record (ADD).

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Store is this package's storage handle: one *sql.DB, already migrated
// via ApplySchema, plus the injected clock Invalidate stamps
// invalidated_at from.
type Store struct {
	db    *sql.DB
	clock runtime.Clock
}

// NewStore returns a Store over db, using clock for every timestamp this
// package's writes need.
func NewStore(db *sql.DB, clock runtime.Clock) *Store {
	return &Store{db: db, clock: clock}
}

// PutClaim inserts a new claim row, or updates an existing one. DataClass
// is immutable (R-21.94): an update whose DataClass differs from the
// stored row's is refused with ErrDataClassImmutable and no columns are
// written.
func (s *Store) PutClaim(ctx context.Context, c Claim) error {
	if c.ID == "" {
		return cascade.New(cascade.KindInvalidInput, "evidence: claim id is required")
	}
	if err := c.Validate(); err != nil {
		return err
	}
	existing, ok, err := s.rawClaim(ctx, c.ID)
	if err != nil {
		return err
	}
	if ok && existing.DataClass != c.DataClass {
		return cascade.Wrapf(cascade.KindConflict, ErrDataClassImmutable,
			"evidence: claim %q data_class is immutable: stored %q, refusing update to %q",
			c.ID, existing.DataClass, c.DataClass)
	}
	verificationJSON, err := json.Marshal(c.Verification)
	if err != nil {
		return cascade.Wrap(cascade.KindInvalidInput, err, "evidence: encode verification")
	}
	contradictionsJSON, err := json.Marshal(c.Contradictions)
	if err != nil {
		return cascade.Wrap(cascade.KindInvalidInput, err, "evidence: encode contradictions")
	}
	invalidatedAt := int64(0)
	if c.InvalidatedAt != nil {
		invalidatedAt = c.InvalidatedAt.Unix()
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO `+tableClaim+` (claim_id, statement, type, confidence, data_class, run_id, lane_id,
			verification_json, contradictions_json, invalidated_at, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(claim_id) DO UPDATE SET statement=excluded.statement, type=excluded.type,
			confidence=excluded.confidence, run_id=excluded.run_id, lane_id=excluded.lane_id,
			verification_json=excluded.verification_json, contradictions_json=excluded.contradictions_json,
			invalidated_at=excluded.invalidated_at`,
		c.ID, c.Statement, string(c.Type), c.Confidence, string(c.DataClass), c.ProducedBy.RunID, c.ProducedBy.LaneID,
		string(verificationJSON), string(contradictionsJSON), invalidatedAt, s.clock.Now().Unix())
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "evidence: put claim")
	}
	return nil
}

// GetClaim reads one claim row by id. A claim with zero
// jobs_claim_evidence rows is never readable as established: this returns
// ErrClaimWithoutEvidence rather than the row.
func (s *Store) GetClaim(ctx context.Context, id string) (Claim, error) {
	c, ok, err := s.rawClaim(ctx, id)
	if err != nil {
		return Claim{}, err
	}
	if !ok {
		return Claim{}, ErrClaimNotFound
	}
	n, err := s.evidenceCount(ctx, id)
	if err != nil {
		return Claim{}, err
	}
	if n == 0 {
		return Claim{}, ErrClaimWithoutEvidence
	}
	return c, nil
}

// ClaimsByRun returns every claim whose ProducedBy.RunID matches runID,
// ordered by claim id, invalidated claims included. Used by index.go's
// ResolveIndex.
func (s *Store) ClaimsByRun(ctx context.Context, runID string) ([]Claim, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT claim_id, statement, type, confidence, data_class, run_id, lane_id,
			verification_json, contradictions_json, invalidated_at
		 FROM `+tableClaim+` WHERE run_id = ? ORDER BY claim_id ASC`, runID)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "evidence: claims by run")
	}
	defer func() { _ = rows.Close() }()
	var out []Claim
	for rows.Next() {
		c, err := scanClaim(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "evidence: iterate claims by run")
	}
	return out, nil
}

// Invalidate stamps invalidated_at from the injected clock. A second call
// on an already-invalidated claim returns ErrClaimInvalidated.
func (s *Store) Invalidate(ctx context.Context, claimID string) error {
	c, ok, err := s.rawClaim(ctx, claimID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrClaimNotFound
	}
	if c.InvalidatedAt != nil {
		return ErrClaimInvalidated
	}
	_, err = s.db.ExecContext(ctx, `UPDATE `+tableClaim+` SET invalidated_at = ? WHERE claim_id = ?`,
		s.clock.Now().Unix(), claimID)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "evidence: invalidate claim")
	}
	return nil
}

// AddContradiction appends contradictingID to claimID's Contradictions
// list.
func (s *Store) AddContradiction(ctx context.Context, claimID, contradictingID string) error {
	c, ok, err := s.rawClaim(ctx, claimID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrClaimNotFound
	}
	c.Contradictions = append(c.Contradictions, contradictingID)
	encoded, err := json.Marshal(c.Contradictions)
	if err != nil {
		return cascade.Wrap(cascade.KindInvalidInput, err, "evidence: encode contradictions")
	}
	_, err = s.db.ExecContext(ctx, `UPDATE `+tableClaim+` SET contradictions_json = ? WHERE claim_id = ?`,
		string(encoded), claimID)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "evidence: add contradiction")
	}
	return nil
}

// AppendVerification appends v to claimID's Verification list. v.Result
// must be one of the closed three values.
func (s *Store) AppendVerification(ctx context.Context, claimID string, v Verification) error {
	if _, err := DecodeVerificationResult(string(v.Result)); err != nil {
		return err
	}
	c, ok, err := s.rawClaim(ctx, claimID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrClaimNotFound
	}
	c.Verification = append(c.Verification, v)
	encoded, err := json.Marshal(c.Verification)
	if err != nil {
		return cascade.Wrap(cascade.KindInvalidInput, err, "evidence: encode verification")
	}
	_, err = s.db.ExecContext(ctx, `UPDATE `+tableClaim+` SET verification_json = ? WHERE claim_id = ?`,
		string(encoded), claimID)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "evidence: append verification")
	}
	return nil
}

// rawClaim reads one claim row without the ErrClaimWithoutEvidence
// established-read check -- used internally by writers that must operate
// on a claim before its first evidence row exists.
func (s *Store) rawClaim(ctx context.Context, id string) (Claim, bool, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT claim_id, statement, type, confidence, data_class, run_id, lane_id,
			verification_json, contradictions_json, invalidated_at
		 FROM `+tableClaim+` WHERE claim_id = ?`, id)
	c, err := scanClaimRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Claim{}, false, nil
	}
	if err != nil {
		if _, ok := cascade.KindOf(err); ok {
			// scanClaimRow's own typed decode error (an unknown stored
			// enum value) -- pass through unchanged rather than
			// reclassifying it as a storage failure.
			return Claim{}, false, err
		}
		return Claim{}, false, cascade.Wrap(cascade.KindUnavailable, err, "evidence: get claim")
	}
	return c, true, nil
}

// rowScanner is the common Scan surface *sql.Row and *sql.Rows both
// satisfy, letting scanClaimRow serve a single-row read (rawClaim) and a
// multi-row iteration (ClaimsByRun) with one implementation.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanClaimRow(row rowScanner) (Claim, error) {
	var c Claim
	var claimType, dataClass, verificationJSON, contradictionsJSON string
	var invalidatedAt int64
	err := row.Scan(&c.ID, &c.Statement, &claimType, &c.Confidence, &dataClass, &c.ProducedBy.RunID,
		&c.ProducedBy.LaneID, &verificationJSON, &contradictionsJSON, &invalidatedAt)
	if err != nil {
		return Claim{}, err
	}
	if c.Type, err = DecodeClaimType(claimType); err != nil {
		return Claim{}, err
	}
	if c.DataClass, err = DecodeDataClass(dataClass); err != nil {
		return Claim{}, err
	}
	if err := json.Unmarshal([]byte(verificationJSON), &c.Verification); err != nil {
		return Claim{}, cascade.Wrap(cascade.KindInternal, err, "evidence: decode verification")
	}
	if err := json.Unmarshal([]byte(contradictionsJSON), &c.Contradictions); err != nil {
		return Claim{}, cascade.Wrap(cascade.KindInternal, err, "evidence: decode contradictions")
	}
	if invalidatedAt != 0 {
		t := unixToTime(invalidatedAt)
		c.InvalidatedAt = &t
	}
	return c, nil
}

func scanClaim(rows *sql.Rows) (Claim, error) {
	c, err := scanClaimRow(rows)
	if err != nil {
		if _, ok := cascade.KindOf(err); ok {
			return Claim{}, err
		}
		return Claim{}, cascade.Wrap(cascade.KindUnavailable, err, "evidence: scan claim row")
	}
	return c, nil
}

func (s *Store) evidenceCount(ctx context.Context, claimID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+tableClaimEvidence+` WHERE claim_id = ?`, claimID).Scan(&n)
	if err != nil {
		return 0, cascade.Wrap(cascade.KindUnavailable, err, "evidence: count claim evidence")
	}
	return n, nil
}
