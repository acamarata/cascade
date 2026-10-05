// Purpose (this file): Resume, the crash-recovery replay of the ci_dispatch
// outbox: after a restart, every interrupted sub-job is dispatched exactly
// once more, every row whose effect already ran is only confirmed, and the
// rows of a tombstoned attempt are dropped.
//
// Inputs: the CI and jobs databases (the dispatch requests recorded on each
// ci_stream_attempt row, and the unconfirmed ci_dispatch outbox rows).
// Outputs: a ResumeReport and the joined errors of any row that could not
// be replayed (the other rows are still replayed).
// Constraints: a confirmed row is never re-dispatched -- a terminal
// sub-job runs once. Intents are first re-ensured for every current
// attempt's recorded dispatches (RecordIntent is a no-op on an existing
// key in any state), which closes the window between recording a dispatch
// and recording its intents. The executor is idempotent per (attempt,
// kind), so "re-dispatch" never writes a second ci_run. A dropped row is
// closed with the outbox's confirm transition (the jobs outbox offers no
// other terminal state to this caller); nothing ran for it.
// SPORT: internal.ci.Dispatcher.Resume/ADDED (P1-CI-01).

package ci

import (
	"context"
	"errors"
	"sort"

	"github.com/acamarata/cascade/internal/jobs"
)

// matchedRow is an unconfirmed outbox row resolved to its recorded dispatch.
type matchedRow struct {
	row     jobs.OutboxRow
	attempt attemptRow
	entry   dispatchEntry
	kind    RequirementKind
}

// Resume replays the unconfirmed ci_dispatch outbox rows.
func (d *Dispatcher) Resume(ctx context.Context) (ResumeReport, error) {
	var rep ResumeReport
	if err := d.ensureCurrentIntents(ctx); err != nil {
		return rep, err
	}
	pending, err := d.unconfirmedKeys(ctx)
	if err != nil {
		return rep, err
	}
	rows := make([]jobs.OutboxRow, 0, len(pending))
	for _, r := range pending {
		rows = append(rows, r)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].CreatedAt != rows[j].CreatedAt {
			return rows[i].CreatedAt < rows[j].CreatedAt
		}
		return rows[i].ID < rows[j].ID
	})
	var errs []error
	touched := map[string]bool{}
	for _, row := range rows {
		m, ok, err := d.matchRow(ctx, row)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if !ok {
			rep.Unmatched++
			continue
		}
		touched[m.attempt.AttemptID] = true
		if err := d.replayRow(ctx, m, &rep); err != nil {
			errs = append(errs, err)
		}
	}
	for id := range touched {
		if err := d.finishAttempt(ctx, id); err != nil {
			errs = append(errs, err)
		}
	}
	return rep, errors.Join(errs...)
}

// replayRow applies Resume's decision to one matched row.
func (d *Dispatcher) replayRow(ctx context.Context, m matchedRow, rep *ResumeReport) error {
	switch {
	case m.attempt.State == attemptTombstoned:
		rep.Dropped++
		return d.confirm(ctx, m.row.IdempotencyKey)
	case m.row.State == jobs.OutboxEffectState:
		rep.Confirmed++
		return d.confirm(ctx, m.row.IdempotencyKey)
	}
	if _, err := d.runKind(ctx, m.entry, m.kind); err != nil {
		if errChainHas(err, ErrAlreadyRunning) {
			return nil
		}
		if errChainHas(err, jobs.ErrLeaseFenced) {
			// The lease was reclaimed: the attempt is tombstoned, nothing ran.
			rep.Dropped++
			return d.confirm(ctx, m.row.IdempotencyKey)
		}
		return err
	}
	rep.Redispatched++
	return nil
}

// ensureCurrentIntents re-records the intents of every recorded dispatch of
// every live attempt; existing keys are left exactly as they are.
func (d *Dispatcher) ensureCurrentIntents(ctx context.Context) error {
	attempts, err := queryAttempts(ctx, d.deps.CIDB, `WHERE state = ?`, attemptLive)
	if err != nil {
		return err
	}
	for _, a := range attempts {
		for _, e := range a.Entries {
			if err := d.ensureIntents(ctx, e); err != nil {
				return err
			}
		}
	}
	return nil
}

// matchRow resolves row to the recorded dispatch and kind whose derived key
// it carries, among the attempts of the row's job.
func (d *Dispatcher) matchRow(ctx context.Context, row jobs.OutboxRow) (matchedRow, bool, error) {
	attempts, err := queryAttempts(ctx, d.deps.CIDB, `WHERE job_id = ?`, row.JobID)
	if err != nil {
		return matchedRow{}, false, err
	}
	for _, a := range attempts {
		for _, e := range a.Entries {
			for _, kind := range e.Kinds {
				if outboxKeyFor(e, kind) == row.IdempotencyKey {
					return matchedRow{row: row, attempt: a, entry: e, kind: kind}, true, nil
				}
			}
		}
	}
	return matchedRow{}, false, nil
}
