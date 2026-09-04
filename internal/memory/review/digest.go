package review

// Purpose: the weekly digest — a pure read of the review queue over an
//   EXPLICIT window, and the scheduler job that fires it once a week and
//   offers the result to an event sink.
// Inputs: the queue's own read path; an injected clock; a cadence.
// Outputs: one memory.MemoryWeeklyDigest per fire.
// Constraints: building a digest writes NOTHING — not a cursor, not a
//   marker, not a receipt. The window is derived from the injected clock
//   and the cadence and is carried in the payload, so the same store and
//   the same clock always produce the same bytes and a reader can always
//   tell which stretch of time a digest speaks for.
// SPORT: event:MemoryWeeklyDigest (ADD, P1-E07-W2-S14-T3).

import (
	"context"
	"time"

	"github.com/acamarata/cascade/internal/memory"
	"github.com/acamarata/cascade/pkg/cascade"
)

// DefaultDigestCadence is the digest's period. It is also the length of
// the window each digest reports on, which is what makes "since the
// previous fire" a statement about time rather than about bookkeeping: no
// cursor is stored anywhere, so a digest cannot be made to lie by a stale
// or lost marker, and re-running one for the same instant recomputes the
// same answer.
const DefaultDigestCadence = 7 * 24 * time.Hour

// ErrInvalidDigestWindow is returned for a window that does not run
// forward in time. A backwards window would silently report nothing and
// look like a quiet week.
var ErrInvalidDigestWindow = cascade.New(cascade.KindInvalidInput,
	"digest window does not run forward")

// DigestWindow is the closed-open stretch of time a digest speaks for.
type DigestWindow struct {
	// Since is the window's inclusive start.
	Since time.Time
	// Until is the window's exclusive end, and the instant the queue's
	// pending state is read at.
	Until time.Time
}

// Digest builds the digest for an explicit window.
//
// It reads and reports; it decides nothing. Pending candidates are the
// queue's LIVE state — below threshold and not snoozed as of the read
// instant, which is what the payload's Until reports — and promoted
// candidates are those whose promotion instant falls inside the window, so
// a promotion is reported in exactly one digest. The window bounds which
// promotions are reported; it does not, and cannot, reconstruct what the
// pending queue looked like at some earlier moment, and the payload says
// Until so no reader has to guess which of the two it is holding.
//
// Nothing here writes. A caller may build the same digest as many times as
// it likes and the store is byte-identical afterwards.
func (q *Queue) Digest(ctx context.Context, w DigestWindow) (memory.MemoryWeeklyDigest, error) {
	if !w.Until.After(w.Since) {
		return memory.MemoryWeeklyDigest{}, cascade.Wrapf(cascade.KindInvalidInput,
			ErrInvalidDigestWindow, "digest window %s..%s does not run forward",
			w.Since.UTC().Format(time.RFC3339), w.Until.UTC().Format(time.RFC3339))
	}
	listed, err := q.List(ctx, ListParams{})
	if err != nil {
		return memory.MemoryWeeklyDigest{}, err
	}
	// Until is the instant the queue was actually read at, not the one the
	// caller asked for: the two differ by however long the read took, and
	// a payload that reported the requested instant would be claiming the
	// pending rows were true at a moment they were not read at.
	window := DigestWindow{Since: w.Since.UTC(), Until: listed.At.UTC()}
	if !window.Until.After(window.Since) {
		return memory.MemoryWeeklyDigest{}, cascade.Wrapf(cascade.KindInvalidInput,
			ErrInvalidDigestWindow, "the queue was read at %s, which is not after the window start %s",
			window.Until.Format(time.RFC3339), window.Since.Format(time.RFC3339))
	}
	out := memory.MemoryWeeklyDigest{
		Since:       window.Since,
		Until:       window.Until,
		MinRefCount: listed.MinRefCount,
		MinSessions: listed.MinSessions,
		Pending:     listed.Pending,
		Promoted:    promotedWithin(listed.Promoted, window),
		Unreadable:  unreadableIDs(listed.Unreadable),
	}
	return out, nil
}

// promotedWithin keeps the promotions whose instant falls inside the
// window. A promoted candidate with no recorded instant is left out rather
// than guessed at: reporting it in every digest, or in none, would both be
// claims the record does not support.
func promotedWithin(
	in []memory.CandidateSummary, w DigestWindow,
) []memory.CandidateSummary {
	out := make([]memory.CandidateSummary, 0, len(in))
	for _, c := range in {
		if c.PromotedAt == nil {
			continue
		}
		at := c.PromotedAt.UTC()
		if at.Before(w.Since.UTC()) || !at.Before(w.Until.UTC()) {
			continue
		}
		out = append(out, c)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// unreadableIDs reduces the unreadable rows to their addresses. The
// refusal text is dropped here on purpose: it is a diagnostic, and a
// diagnostic can name a machine path, which must not travel on a fan-out
// bus. The address alone is what a reader needs to go and look.
func unreadableIDs(in []Unreadable) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, u := range in {
		out = append(out, u.ID)
	}
	return out
}

// DigestJob is the scheduled weekly digest: one read, one event, no
// writes.
type DigestJob struct {
	queue   *Queue
	cadence time.Duration
	sink    memory.DigestEventSink
}

// NewDigestJob returns a job over q firing on cadence and publishing to
// sink. A non-positive cadence resolves to DefaultDigestCadence, and a nil
// sink discards the event — both are supported configurations, and
// neither changes what the job reads or writes (which is: everything, and
// nothing).
func NewDigestJob(q *Queue, cadence time.Duration, sink memory.DigestEventSink) *DigestJob {
	if cadence <= 0 {
		cadence = DefaultDigestCadence
	}
	if sink == nil {
		sink = memory.DiscardDigestEvents()
	}
	return &DigestJob{queue: q, cadence: cadence, sink: sink}
}

// Run builds one digest for the window ending now and offers it to the
// sink exactly once.
//
// It publishes even when the digest is empty. A weekly event that appears
// only when there is news is indistinguishable from a job that stopped
// running, and the whole point of the cadence is that a user does not have
// to poll to find out.
func (j *DigestJob) Run(ctx context.Context) (memory.MemoryWeeklyDigest, error) {
	now := j.queue.clock.Now().UTC()
	digest, err := j.queue.Digest(ctx, DigestWindow{Since: now.Add(-j.cadence), Until: now})
	if err != nil {
		return memory.MemoryWeeklyDigest{}, err
	}
	if err := j.sink.MemoryWeeklyDigestReady(ctx, digest); err != nil {
		return digest, err
	}
	return digest, nil
}
