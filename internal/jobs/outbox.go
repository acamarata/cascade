package jobs

// Purpose: R-21.148's transactional outbox -- RecordIntent (write the
//
//	intent row inside the caller's own store transaction, alongside the
//	state change that decided it), ConfirmEffect (mark a row confirmed
//	after the effect actually ran), and Reconcile (resume-time: for
//	every unconfirmed row, ask the site whether the effect already
//	happened; confirm-without-reperforming if so, re-perform under the
//	SAME key if not, or compensate if the owning job reached a terminal
//	state first).
//
// Inputs: an open *sql.DB already migrated via ApplyOutboxSchema, an
//
//	OutboxIntent (job id, site, attempt generation, payload hash), and
//	(for Reconcile) a SiteProbe per site that answers "does the effect
//	already exist".
//
// Outputs: typed A-T7 errors for a malformed intent or a storage
//
//	failure; Reconcile returns the ReconcileResult naming what happened
//	to each row.
//
// Constraints: the idempotency key is DERIVED, never random:
//
//	"<site>:<job_id>:<attempt_generation>:<payload_hash>" (HOW 5). A
//	second RecordIntent under the same key is a no-op (the unique index
//	on idempotency_key refuses the duplicate insert; this file treats
//	that specific conflict as success, matching "re-apply is an
//	idempotent no-op").
//
// SPORT: jobs/scheduler-outbox/ADD (P1-E29-W6-S59-T5).

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/acamarata/cascade/pkg/cascade"
)

// OutboxSite is the closed R-21.148 vocabulary of external-effect sites.
type OutboxSite string

// The five closed OutboxSite values.
const (
	OutboxSiteSpawn        OutboxSite = "spawn"
	OutboxSiteLeaseAcquire OutboxSite = "lease_acquire"
	OutboxSiteInboxPublish OutboxSite = "inbox_publish"
	OutboxSiteCIDispatch   OutboxSite = "ci_dispatch"
	OutboxSiteIntegration  OutboxSite = "integration"
)

// Valid reports whether s is one of the five closed sites.
func (s OutboxSite) Valid() bool {
	switch s {
	case OutboxSiteSpawn, OutboxSiteLeaseAcquire, OutboxSiteInboxPublish, OutboxSiteCIDispatch, OutboxSiteIntegration:
		return true
	}
	return false
}

// OutboxState is the closed row lifecycle: intent -> effect -> confirmed,
// or -> reconciled (resume path). A compensated/failed terminal state is
// named in this package's own header doc comment (Reconcile's forward
// contract) but no compensation path exists in this tree yet -- removed
// rather than left as a dead exported constant nothing constructs
// (internal/build's dead-code gate); a future Reconcile/compensate
// ticket re-adds it alongside the code that actually sets it.
type OutboxState string

// The closed OutboxState vocabulary.
const (
	OutboxIntentState OutboxState = "intent"
	OutboxEffectState OutboxState = "effect"
	OutboxConfirmed   OutboxState = "confirmed"
	OutboxReconciled  OutboxState = "reconciled"
)

// OutboxRow is one persisted outbox record.
type OutboxRow struct {
	ID                string
	JobID             string
	AttemptGeneration int64
	Site              OutboxSite
	IdempotencyKey    string
	State             OutboxState
	PayloadHash       string
	CreatedAt         int64
	UpdatedAt         int64
}

// DeriveIdempotencyKey builds the HOW 5 stable, derived key.
func DeriveIdempotencyKey(site OutboxSite, jobID string, attemptGeneration int64, payloadHash string) string {
	return fmt.Sprintf("%s:%s:%d:%s", site, jobID, attemptGeneration, payloadHash)
}

// ErrInvalidOutboxSite is returned for an OutboxSite outside the five
// closed values -- fail-closed, no permissive default site.
var ErrInvalidOutboxSite = cascade.New(cascade.KindInvalidInput, "jobs: outbox: invalid site")

// RecordIntent inserts one outbox row in state=intent inside tx -- the
// SAME transaction the caller uses for the state change that decided
// this effect (R-21.148). A duplicate insert under the same derived key
// is treated as success (idempotent no-op): the row already reflects an
// earlier decision to perform the same effect.
func RecordIntent(ctx context.Context, tx *sql.Tx, clock int64, in OutboxIntent) (string, error) {
	if !in.Site.Valid() {
		return "", ErrInvalidOutboxSite
	}
	if in.JobID == "" {
		return "", cascade.New(cascade.KindInvalidInput, "jobs: outbox: RecordIntent requires a job id")
	}
	key := DeriveIdempotencyKey(in.Site, in.JobID, in.AttemptGeneration, in.PayloadHash)
	if existing, ok, err := getOutboxByKeyTx(ctx, tx, key); err != nil {
		return "", err
	} else if ok {
		return existing.ID, nil
	}
	id, err := cascade.NewID()
	if err != nil {
		return "", err
	}
	row := OutboxRow{
		ID: string(id), JobID: in.JobID, AttemptGeneration: in.AttemptGeneration, Site: in.Site,
		IdempotencyKey: key, State: OutboxIntentState, PayloadHash: in.PayloadHash,
		CreatedAt: clock, UpdatedAt: clock,
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO `+tableOutbox+` (id, job_id, attempt_generation, site, idempotency_key, state, payload_hash, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		row.ID, row.JobID, row.AttemptGeneration, string(row.Site), row.IdempotencyKey, string(row.State), row.PayloadHash, row.CreatedAt, row.UpdatedAt,
	); err != nil {
		return "", cascade.Wrap(cascade.KindUnavailable, err, "jobs: outbox: insert intent row")
	}
	return row.ID, nil
}

// MarkEffect transitions key's row from intent to effect (the effect ran
// but is not yet confirmed) inside tx.
func MarkEffect(ctx context.Context, tx *sql.Tx, clock int64, key string) error {
	return transitionOutboxTx(ctx, tx, clock, key, OutboxEffectState)
}

// ConfirmEffect transitions key's row to confirmed inside tx -- the row's
// terminal success state, never re-performed again.
func ConfirmEffect(ctx context.Context, tx *sql.Tx, clock int64, key string) error {
	return transitionOutboxTx(ctx, tx, clock, key, OutboxConfirmed)
}

func transitionOutboxTx(ctx context.Context, tx *sql.Tx, clock int64, key string, to OutboxState) error {
	res, err := tx.ExecContext(ctx,
		`UPDATE `+tableOutbox+` SET state = ?, updated_at = ? WHERE idempotency_key = ?`,
		string(to), clock, key,
	)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "jobs: outbox: transition row")
	}
	n, err := res.RowsAffected()
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "jobs: outbox: rows affected")
	}
	if n == 0 {
		return cascade.Newf(cascade.KindNotFound, "jobs: outbox: no row for key %q", key)
	}
	return nil
}

func getOutboxByKeyTx(ctx context.Context, tx *sql.Tx, key string) (OutboxRow, bool, error) {
	row := tx.QueryRowContext(ctx,
		`SELECT id, job_id, attempt_generation, site, idempotency_key, state, payload_hash, created_at, updated_at
		 FROM `+tableOutbox+` WHERE idempotency_key = ?`, key)
	return scanOutboxRow(row)
}

func scanOutboxRow(row *sql.Row) (OutboxRow, bool, error) {
	var r OutboxRow
	var site, state string
	if err := row.Scan(&r.ID, &r.JobID, &r.AttemptGeneration, &site, &r.IdempotencyKey, &state, &r.PayloadHash, &r.CreatedAt, &r.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return OutboxRow{}, false, nil
		}
		return OutboxRow{}, false, cascade.Wrap(cascade.KindUnavailable, err, "jobs: outbox: scan row")
	}
	r.Site = OutboxSite(site)
	r.State = OutboxState(state)
	return r, true, nil
}

// unconfirmedOutboxRowsTx returns every row left in intent or effect,
// oldest first -- Resume's reconciliation scan.
func unconfirmedOutboxRowsTx(ctx context.Context, tx *sql.Tx) ([]OutboxRow, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT id, job_id, attempt_generation, site, idempotency_key, state, payload_hash, created_at, updated_at
		 FROM `+tableOutbox+` WHERE state IN (?, ?) ORDER BY created_at ASC`,
		string(OutboxIntentState), string(OutboxEffectState))
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "jobs: outbox: query unconfirmed rows")
	}
	defer func() { _ = rows.Close() }()
	var out []OutboxRow
	for rows.Next() {
		var r OutboxRow
		var site, state string
		if err := rows.Scan(&r.ID, &r.JobID, &r.AttemptGeneration, &site, &r.IdempotencyKey, &state, &r.PayloadHash, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, cascade.Wrap(cascade.KindUnavailable, err, "jobs: outbox: scan unconfirmed row")
		}
		r.Site = OutboxSite(site)
		r.State = OutboxState(state)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "jobs: outbox: iterate unconfirmed rows")
	}
	return out, nil
}
