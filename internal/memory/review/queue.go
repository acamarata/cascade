// Package review is the human lane over the memory candidate ledger.
//
// The mechanical lane (internal/memory's PromotionLadder) promotes a
// candidate the moment it crosses the Q-6 thresholds, with no model call
// and no user prompt. This package is the complement: it SHOWS what that
// lane decided and what it has not yet decided, and it carries out the
// four actions a human can take about it — approve a below-threshold
// candidate early, skip it, defer it, or revert a promotion.
//
// Two rules shape everything here.
//
// Reading is never deciding. Every read path in this package — the
// listing, the digest — writes nothing at all. Rendering a queue or
// building a weekly digest leaves the store byte-identical, so no user can
// promote or retire anything by looking at it.
//
// Reporting is never recommending. A pending candidate is listed because
// its count is BELOW the mechanical threshold, which is a fact about the
// evidence; the thresholds travel with every result so a reader can check
// that against the counts rather than take the word "pending" on trust.
// Nothing in this package ranks, scores or suggests.
//
// Purpose: this file holds the queue type, the ledger contract it needs,
//
//	and the read path (Snapshot/List).
//
// Inputs: candidate state read through the ledger; an injected clock, which
//
//	is the only source of "now" a snooze is evaluated against.
//
// Outputs: a listing of candidate summaries, or a typed pkg/cascade error.
// Constraints: no bare time.Now; no write on any read path; a candidate
//
//	that cannot be read is NAMED rather than dropped, since a queue that
//	silently omits a damaged candidate reports a smaller queue than the one
//	on disk.
//
// SPORT: internal/memory/review (ADD, P1-E07-W2-S14-T3).
package review

import (
	"context"
	"sort"
	"time"

	"github.com/acamarata/cascade/internal/memory"
	"github.com/acamarata/cascade/pkg/cascade"
)

// The two sections of the review queue. A caller may ask for one of them
// or, with the empty string, for both.
const (
	// SectionPending names the below-threshold candidates awaiting a
	// human decision.
	SectionPending = "pending"
	// SectionPromoted names the promotions still standing, which are the
	// ones a revert can take back.
	SectionPromoted = "promoted"
)

// ErrUnknownSection is returned for a section outside the two names above.
// It fails closed rather than defaulting to "both": a caller that asked
// for a section this build does not know must not be handed a listing it
// did not ask for and may act on.
var ErrUnknownSection = cascade.New(cascade.KindInvalidInput,
	"unknown review section")

// Ledger is the slice of the candidate ledger this package needs.
//
// It is declared here, at the point of use, rather than taking
// memory.CandidateLedger whole: the review queue never records an
// observation, and a contract that let it would put the human lane and the
// mechanical counting lane in one place. Defer is not on
// memory.CandidateLedger at all — it is the review action's own write, and
// *memory.FileCandidateLedger is what satisfies this.
type Ledger interface {
	// List returns the candidate names of one kind, lexically ordered.
	List(ctx context.Context, kind memory.MemoryKind) ([]string, error)
	// Get returns one candidate's state.
	Get(ctx context.Context, kind memory.MemoryKind, name string) (memory.CandidateEntry, error)
	// Promote makes a candidate's draft durable.
	Promote(ctx context.Context, kind memory.MemoryKind, name string) (memory.CandidateEntry, error)
	// Revert takes a promotion back.
	Revert(ctx context.Context, kind memory.MemoryKind, name, reason string) (memory.CandidateEntry, error)
	// Defer hides a pending candidate from the queue until an instant.
	Defer(ctx context.Context, kind memory.MemoryKind, name string, until time.Time) (memory.CandidateEntry, error)
}

// Unreadable names a candidate record that could not be read whole.
type Unreadable struct {
	// ID is the canonical address of the candidate.
	ID string `json:"id"`
	// Reason is the refusal, as the ledger reported it.
	Reason string `json:"reason"`
}

// ListParams is memory.review.list's input.
type ListParams struct {
	// Section restricts the listing to one section. Empty means both.
	Section string `json:"section,omitempty"`
}

// ListResult is memory.review.list's output.
//
// It carries the thresholds and the instant it was read for, not only the
// rows: "below threshold" and "not snoozed" are claims about numbers and
// about a moment, and a result that stated them without the numbers or the
// moment would be asking to be believed.
type ListResult struct {
	// At is the instant the queue was read, from the injected clock. A
	// snooze is evaluated against exactly this instant.
	At time.Time `json:"at"`
	// MinRefCount and MinSessions are the Q-6 thresholds in force.
	MinRefCount int `json:"min_ref_count"`
	MinSessions int `json:"min_sessions"`
	// Pending are below-threshold, non-snoozed candidates in canonical
	// address order.
	Pending []memory.CandidateSummary `json:"pending"`
	// Promoted are the promotions still standing, in canonical address
	// order.
	Promoted []memory.CandidateSummary `json:"promoted"`
	// Snoozed counts pending candidates hidden by a live snooze. It is a
	// count and not rows: a deferred candidate was deliberately taken off
	// the screen, and listing them anyway would undo the action, while
	// saying nothing at all would let a queue look empty when it is not.
	Snoozed int `json:"snoozed"`
	// DueForAutoPromotion are pending candidates that have ALREADY crossed
	// the thresholds. They are not review items — the mechanical lane owns
	// them — and are reported separately so that a candidate stuck above
	// the threshold is visible rather than silently absent from both
	// sections.
	DueForAutoPromotion []memory.CandidateSummary `json:"due_for_auto_promotion,omitempty"`
	// Unreadable names candidates that could not be read.
	Unreadable []Unreadable `json:"unreadable,omitempty"`
}

// Queue is the review surface over one candidate ledger.
type Queue struct {
	ledger Ledger
	clock  memory.Clock
	sink   ActionEventSink
}

// NewQueue returns a queue over ledger, taking its timestamps from clk.
// Actions are reported to sink; a nil sink discards them, which is the
// supported no-bus configuration and never changes what is stored.
func NewQueue(ledger Ledger, clk memory.Clock, sink ActionEventSink) *Queue {
	if sink == nil {
		sink = discardActionEvents{}
	}
	return &Queue{ledger: ledger, clock: clk, sink: sink}
}

// snapshot is the whole queue as one read, before any section filter.
type snapshot struct {
	pending    []memory.CandidateEntry
	promoted   []memory.CandidateEntry
	unreadable []Unreadable
}

// read walks every kind and reads every candidate, sorting each candidate
// into exactly one bucket.
//
// It writes nothing. A candidate whose record cannot be read is recorded
// in unreadable and the walk continues, matching the ledger's own listing
// rule: one damaged file must never make the rest of a kind invisible.
func (q *Queue) read(ctx context.Context) (snapshot, error) {
	var out snapshot
	for _, kind := range memory.AllKinds() {
		names, err := q.ledger.List(ctx, kind)
		if err != nil {
			return snapshot{}, err
		}
		for _, name := range names {
			entry, err := q.ledger.Get(ctx, kind, name)
			if err != nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return snapshot{}, err
				}
				out.unreadable = append(out.unreadable,
					Unreadable{ID: memory.Address(kind, name), Reason: err.Error()})
				continue
			}
			switch entry.Status {
			case memory.CandidatePending:
				out.pending = append(out.pending, entry)
			case memory.CandidatePromoted:
				out.promoted = append(out.promoted, entry)
			case memory.CandidateReverted:
				// A reverted candidate is neither awaiting a decision nor
				// a promotion that can be taken back. It reappears as
				// pending the next time it is observed (R-14.22).
			}
		}
	}
	sortUnreadable(out.unreadable)
	return out, nil
}

// List returns the review queue as of the injected clock's now.
//
// It is a pure read: nothing in this path writes to the store, so
// rendering the queue can never promote, retire or hide anything.
func (q *Queue) List(ctx context.Context, p ListParams) (ListResult, error) {
	if err := validateSection(p.Section); err != nil {
		return ListResult{}, err
	}
	now := q.clock.Now().UTC()
	snap, err := q.read(ctx)
	if err != nil {
		return ListResult{}, err
	}
	out := ListResult{
		At:          now,
		MinRefCount: memory.PromotionMinRefCount,
		MinSessions: memory.PromotionMinSessions,
		Unreadable:  snap.unreadable,
	}
	if p.Section == "" || p.Section == SectionPending {
		out.Pending, out.DueForAutoPromotion, out.Snoozed = partitionPending(snap.pending, now)
	}
	if p.Section == "" || p.Section == SectionPromoted {
		out.Promoted = summarize(snap.promoted)
	}
	return out, nil
}

// partitionPending splits pending candidates into the ones a human is
// being shown, the ones the mechanical lane owns, and a count of the ones
// a live snooze is hiding.
func partitionPending(
	entries []memory.CandidateEntry, now time.Time,
) (shown, due []memory.CandidateSummary, snoozed int) {
	for _, e := range entries {
		if memory.ReadyForPromotion(e) {
			due = append(due, memory.SummarizeCandidate(e))
			continue
		}
		if e.SnoozeUntil != nil && now.Before(*e.SnoozeUntil) {
			snoozed++
			continue
		}
		shown = append(shown, memory.SummarizeCandidate(e))
	}
	sortSummaries(shown)
	sortSummaries(due)
	return shown, due, snoozed
}

// summarize projects entries into summaries in canonical address order.
func summarize(entries []memory.CandidateEntry) []memory.CandidateSummary {
	out := make([]memory.CandidateSummary, 0, len(entries))
	for _, e := range entries {
		out = append(out, memory.SummarizeCandidate(e))
	}
	sortSummaries(out)
	if len(out) == 0 {
		return nil
	}
	return out
}

// sortSummaries orders summaries by canonical address, so identical store
// state always renders identical bytes.
func sortSummaries(in []memory.CandidateSummary) {
	sort.Slice(in, func(i, j int) bool { return in[i].ID < in[j].ID })
}

// sortUnreadable orders unreadable rows by address, for the same reason.
func sortUnreadable(in []Unreadable) {
	sort.Slice(in, func(i, j int) bool { return in[i].ID < in[j].ID })
}

// validateSection refuses a section name this build does not know.
func validateSection(s string) error {
	switch s {
	case "", SectionPending, SectionPromoted:
		return nil
	default:
		return cascade.Wrapf(cascade.KindInvalidInput, ErrUnknownSection,
			"unknown review section %q, want %q or %q", s, SectionPending, SectionPromoted)
	}
}
