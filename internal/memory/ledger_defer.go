package memory

// Purpose: Defer, the ledger write the review queue's defer action makes.
//   CandidateEntry.SnoozeUntil is the field ledger.go reserves for exactly
//   this action; this file is the only thing that sets it, so the queue
//   never reaches around the ledger to edit a candidate file itself.
// Inputs: a candidate address and the instant the review becomes due
//   again, already computed by the caller from its injected clock.
// Outputs: the candidate's state afterwards, or a typed pkg/cascade error.
// Constraints: a defer changes WHEN a candidate is shown and nothing else
//   — never its counts, its status, or its draft — so a deferred candidate
//   that later crosses the Q-6 threshold still promotes mechanically on
//   schedule. Only a pending candidate can be deferred; hiding a promoted
//   one would hide a durable record from the only surface that can take it
//   back.
// SPORT: internal.memory.FileCandidateLedger.Defer (ADD, P1-E07-W2-S14-T3).

import (
	"context"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// ErrNotPending is returned by Defer for a candidate that is not pending.
//
// It is a conflict rather than a silent success for the reason
// ErrAlreadyPromoted is: a caller deferring a promoted candidate has lost
// track of the state, and quietly doing nothing would leave it believing a
// durable record had been hidden from review when it had not.
var ErrNotPending = cascade.New(cascade.KindConflict, "memory candidate is not pending")

// ErrSnoozeInThePast is returned when the requested due instant is not
// after now. A defer that has already expired is not a deferral; accepting
// one would let a caller record an action that changed nothing while
// reporting that something happened.
var ErrSnoozeInThePast = cascade.New(cascade.KindInvalidInput,
	"snooze instant is not in the future")

// Defer hides a pending candidate from the review queue until until.
//
// It writes CandidateEntry.SnoozeUntil and nothing else. In particular it
// does not touch RefCount, SessionIDs or Status: the mechanical promotion
// lane counts the same evidence it counted before, and a deferred
// candidate that reaches the Q-6 thresholds is promoted on the same terms
// as any other. Deferring is a statement about a human's attention, not
// about the evidence.
func (l *FileCandidateLedger) Defer(
	ctx context.Context, kind MemoryKind, name string, until time.Time,
) (CandidateEntry, error) {
	if err := ctx.Err(); err != nil {
		return CandidateEntry{}, cascade.Wrap(cascade.KindCanceled, err, "candidate defer canceled")
	}
	now := l.clock.Now().UTC()
	if !until.After(now) {
		return CandidateEntry{}, cascade.Wrapf(cascade.KindInvalidInput, ErrSnoozeInThePast,
			"snooze until %s is not after %s", until.UTC().Format(time.RFC3339),
			now.Format(time.RFC3339))
	}
	rec, err := l.mustLoad(kind, name)
	if err != nil {
		return CandidateEntry{}, err
	}
	if rec.Status != string(CandidatePending) {
		return CandidateEntry{}, cascade.Wrapf(cascade.KindConflict, ErrNotPending,
			"memory candidate %s/%s is %s, not pending", kind, name, rec.Status)
	}
	snooze := until.UTC()
	rec.SnoozeUntil = &snooze
	rec.UpdatedAt = now
	if err := l.persist(rec); err != nil {
		return CandidateEntry{}, err
	}
	return rec.canonical().view(), nil
}
