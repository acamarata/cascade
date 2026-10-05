// Purpose (this file): the dispatcher's use of the jobs outbox (R-21.148):
// one ci_dispatch intent per requirement kind, then effect and confirm
// transitions, all through the internal/jobs implementation (a second
// outbox is prohibited, C5).
//
// Inputs: a dispatchEntry (the ref, snapshot and kind list of one
// Dispatch request) and the jobs database.
// Outputs: derived idempotency keys and outbox transitions.
// Constraints: the key is jobs.DeriveIdempotencyKey over
// (site, job, attempt generation, payload hash); the payload hash binds
// the attempt id, the kind and the acceptance flag, so a re-dispatch under
// a new attempt or a different kind never collides. RecordIntent is an
// idempotent no-op on an existing key, which is what makes ensureIntents
// safe to repeat after a crash. Order matters for crash safety: intent
// before the effect, effect before confirm, and a row is confirmed only
// after the sub-job's ci_run rows are committed.
// SPORT: internal.ci.outbox/ADDED (P1-CI-01).

package ci

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"strconv"

	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/pkg/cascade"
)

// payloadHashFor is hex(sha256(attempt|kind|acceptance)).
func payloadHashFor(attemptID string, kind RequirementKind, acceptance bool) string {
	sum := sha256.Sum256([]byte(attemptID + "|" + string(kind) + "|" + strconv.FormatBool(acceptance)))
	return hex.EncodeToString(sum[:])
}

// outboxKeyFor is the derived idempotency key of one (entry, kind).
func outboxKeyFor(e dispatchEntry, kind RequirementKind) string {
	return jobs.DeriveIdempotencyKey(jobs.OutboxSiteCIDispatch, e.Ref.JobID, e.Ref.AttemptGeneration,
		payloadHashFor(e.Snapshot.AttemptID, kind, e.Acceptance))
}

// intentFor is the outbox intent of one (entry, kind).
func intentFor(e dispatchEntry, kind RequirementKind) jobs.OutboxIntent {
	return jobs.OutboxIntent{
		JobID: e.Ref.JobID, Site: jobs.OutboxSiteCIDispatch, AttemptGeneration: e.Ref.AttemptGeneration,
		PayloadHash: payloadHashFor(e.Snapshot.AttemptID, kind, e.Acceptance),
	}
}

// outboxTx runs fn inside one jobs-database transaction.
func (d *Dispatcher) outboxTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := d.deps.JobsDB.BeginTx(ctx, nil)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "ci: stream: begin outbox transaction")
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "ci: stream: commit outbox transaction")
	}
	return nil
}

// ensureIntents records one intent per kind of e in one transaction. A key
// that already exists (in any state) is left exactly as it is.
func (d *Dispatcher) ensureIntents(ctx context.Context, e dispatchEntry) error {
	return d.outboxTx(ctx, func(tx *sql.Tx) error {
		for _, kind := range e.Kinds {
			if _, err := jobs.RecordIntent(ctx, tx, d.nowMillis(), intentFor(e, kind)); err != nil {
				return err
			}
		}
		return nil
	})
}

// markEffect moves key's row from intent to effect.
func (d *Dispatcher) markEffect(ctx context.Context, key string) error {
	return d.outboxTx(ctx, func(tx *sql.Tx) error { return jobs.MarkEffect(ctx, tx, d.nowMillis(), key) })
}

// confirm moves key's row to its terminal confirmed state.
func (d *Dispatcher) confirm(ctx context.Context, key string) error {
	return d.outboxTx(ctx, func(tx *sql.Tx) error { return jobs.ConfirmEffect(ctx, tx, d.nowMillis(), key) })
}

// unconfirmedKeys is the set of ci_dispatch keys still in intent or effect.
func (d *Dispatcher) unconfirmedKeys(ctx context.Context) (map[string]jobs.OutboxRow, error) {
	rows, err := jobs.UnconfirmedOutbox(ctx, d.deps.JobsDB, jobs.OutboxSiteCIDispatch)
	if err != nil {
		return nil, err
	}
	out := make(map[string]jobs.OutboxRow, len(rows))
	for _, r := range rows {
		out[r.IdempotencyKey] = r
	}
	return out, nil
}
