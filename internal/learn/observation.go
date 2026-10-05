package learn

// Purpose: the observation write path of the capability scorer. Observe
//   folds one success or failure into the decayed (alpha, beta) mass of one
//   (scope_key, task_class, tier) row; observationFor decides which outcome
//   is observable and under which key, so OutcomeWriter.Record calls Observe
//   exactly once per recorded terminal outcome.
// Inputs: a ScopeKey, task class, tier and success flag (Observe); a
//   TelemetryOutcome (observationFor).
// Outputs: one upserted jobs_capability_score_observations row.
// Constraints: the previous mass is decayed to NOW before the new
//   observation is added (a future last_updated is not amplified), inside
//   one BEGIN IMMEDIATE transaction on a dedicated connection, so two
//   writers on a multi-connection pool serialise and none fails or loses an
//   update; a cell with no prior is refused (KindInvalidInput) because no
//   posterior can read it back; the stored mass is observed counts only,
//   never the prior.
// SPORT: internal.learn.ObservationWriter/ADDED (P1-CAP-03).

import (
	"context"
	"database/sql"
	"time"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/fleet/capacity"
	"github.com/acamarata/cascade/pkg/cascade"
)

// ObservationWriter folds one terminal outcome into a capability posterior.
// SQLiteCapabilityScorer is the production implementation.
type ObservationWriter interface {
	Observe(ctx context.Context, scope ScopeKey, tc conductor.TaskClass, tier capacity.Tier, success bool) error
}

var _ ObservationWriter = (*SQLiteCapabilityScorer)(nil)

// Observe adds one observation (success or failure) at scope for (tc, tier).
func (s *SQLiteCapabilityScorer) Observe(ctx context.Context, scope ScopeKey, tc conductor.TaskClass, tier capacity.Tier, success bool) error {
	if s == nil || s.db == nil || s.clock == nil {
		return cascade.New(cascade.KindInvalidInput, "learn: Observe requires a constructed SQLiteCapabilityScorer")
	}
	if _, err := ParseScopeKey(string(scope)); err != nil {
		return err
	}
	if _, _, ok := PriorAlphaBeta(tier, tc); !ok {
		return cascade.New(cascade.KindInvalidInput, "learn: cannot observe a (tier, task class) cell that has no capability prior")
	}
	now := s.clock.Now().UTC()
	return withImmediateTx(ctx, s.db, func(q *sql.Conn) error {
		row, _, err := readRow(ctx, q, scope, tc, tier)
		if err != nil {
			return err
		}
		next := foldObservation(row, success, now)
		if _, err := q.ExecContext(ctx, `INSERT INTO `+tableCapabilityScore+`
		(scope_key, task_class, tier, alpha, beta, observation_count, last_updated) VALUES (?,?,?,?,?,?,?)
		ON CONFLICT(scope_key, task_class, tier) DO UPDATE SET
		alpha = excluded.alpha, beta = excluded.beta,
		observation_count = excluded.observation_count, last_updated = excluded.last_updated`,
			string(scope), string(tc), string(tier), next.alpha, next.beta, next.count, next.lastUpdated.Unix()); err != nil {
			return cascade.Wrap(cascade.KindUnavailable, err, "learn: upsert jobs_capability_score_observations row")
		}
		return nil
	})
}

// beginStatement opens the observation transaction. It must take the write
// lock at BEGIN: a deferred BEGIN reads first and then fails to upgrade with
// SQLITE_BUSY when another connection writes in between.
const beginStatement = "BEGIN IMMEDIATE"

// withImmediateTx runs fn on one dedicated connection inside a write
// transaction that holds the write lock from the first statement, so a
// concurrent writer waits (busy_timeout) instead of failing the upgrade. fn
// error or a failed COMMIT rolls back; the connection is always released
// with no transaction left open.
func withImmediateTx(ctx context.Context, db *sql.DB, fn func(*sql.Conn) error) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "learn: take an observation connection")
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, beginStatement); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "learn: begin observation transaction")
	}
	if err := fn(conn); err != nil {
		_, _ = conn.ExecContext(context.WithoutCancel(ctx), "ROLLBACK")
		return err
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		_, _ = conn.ExecContext(context.WithoutCancel(ctx), "ROLLBACK")
		return cascade.Wrap(cascade.KindUnavailable, err, "learn: commit observation")
	}
	return nil
}

// foldObservation decays row (the zero row decays to zero) to now and adds
// one success or failure.
func foldObservation(row scoreRow, success bool, now time.Time) scoreRow {
	next := scoreRow{
		alpha: decayed(row.alpha, row.lastUpdated, now), beta: decayed(row.beta, row.lastUpdated, now),
		count: row.count + 1, lastUpdated: now,
	}
	if success {
		next.alpha++
	} else {
		next.beta++
	}
	return next
}

// observationFor maps a recorded outcome to its observation: scope
// repo:<RepoID>, the outcome's task class and lane tier, success = accepted.
// ok is false when the outcome cannot name a scored cell: an unknown or empty
// repo id (the reconciler's neutral default would pool every repo under one
// key), a lane tier that is not a capacity.Tier, or a task class with no
// prior. Such an outcome is still recorded; it just feeds no posterior.
func observationFor(o TelemetryOutcome) (scope ScopeKey, tc conductor.TaskClass, tier capacity.Tier, success, ok bool) {
	tc, tier = conductor.TaskClass(o.TaskClass), capacity.Tier(o.LaneTier)
	if o.RepoID == "" || o.RepoID == "unknown" || !tier.Valid() {
		return "", "", "", false, false
	}
	if _, _, has := PriorAlphaBeta(tier, tc); !has {
		return "", "", "", false, false
	}
	return repoScope(o.RepoID), tc, tier, o.FinalOutcome == OutcomeAccepted, true
}
