// Package pews (lifecycle_apply.go): applyEvent is Claim/Step/RecordCR/
// RecordQA/Done's shared core (lifecycle.go), split into its own file
// because the CASAppender change (lifecycle_cas.go) would have pushed
// lifecycle.go past the repo's 300-line gate. Claim is the one event that
// requires a CASAppender: a plain Append races (two concurrent claims can
// both see StateUnclaimed and both succeed), while step/cr/qa/done only
// ever run against an already-claimed, single-owner ticket and keep their
// existing Append behavior unchanged (AC16, this ticket's journal).
// SPORT: plugins/pbd/internal/pews lifecycle_apply (ADD) — P1-PBD-07.
package pews

import (
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/pkg/cascade"
)

// applyEvent refuses a nil tree or unknown ticket id, then dispatches:
// Claim goes through applyClaim's single-Replay-snapshot CAS path (below);
// every other event replays current state through CurrentState (its own,
// independent Replay call is safe here — step/cr/qa/done never race a
// concurrent winner the way a fresh claim does), checks the transition,
// and appends unchanged via js.Append.
func applyEvent(ctx context.Context, tree *Tree, js JournalStore, ticketID, operationID string, event LifecycleEvent, payload json.RawMessage) (JournalEntry, error) {
	if tree == nil {
		return JournalEntry{}, cascade.New(cascade.KindInvalidInput, "pews: cannot apply a lifecycle event to a nil tree")
	}
	if _, err := findTicket(tree, ticketID); err != nil {
		return JournalEntry{}, err
	}
	if event == EventClaim {
		if err := RequireBuildable(tree); err != nil {
			return JournalEntry{}, err
		}
		return applyClaim(ctx, js, ticketID, operationID, payload)
	}
	current, err := CurrentState(ctx, js, ticketID)
	if err != nil {
		return JournalEntry{}, err
	}
	if _, terr := nextLifecycleState(current, event); terr != nil {
		return JournalEntry{}, terr
	}
	return js.Append(ctx, ticketID, event, operationID, payload)
}

// applyClaim is applyEvent's Claim-only path. It replays ticketID ONCE,
// deriving both the transition-legality check and CASAppender's
// expectedSeq from that SAME snapshot — two separate Replay calls (one via
// CurrentState, one to compute expectedSeq) would let a concurrent
// winner's append land between them, pairing a stale "Unclaimed ->
// Claimed is legal" verdict with a freshly-re-read (now matching, now
// WRONG) expectedSeq, letting two claims both win (proven RED by this
// ticket's mutation evidence). A single snapshot closes that window: if it
// goes stale, AppendIf's own fresh load under its lock disagrees and
// refuses. It refuses KindUnsupported when js is not a CASAppender, before
// any append.
func applyClaim(ctx context.Context, js JournalStore, ticketID, operationID string, payload json.RawMessage) (JournalEntry, error) {
	entries, err := js.Replay(ctx, ticketID)
	if err != nil {
		return JournalEntry{}, err
	}
	state, serr := deriveState(ticketID, entries)
	if serr != nil {
		return JournalEntry{}, serr
	}
	if _, terr := nextLifecycleState(state, EventClaim); terr != nil {
		return JournalEntry{}, terr
	}
	cas, ok := js.(CASAppender)
	if !ok {
		return JournalEntry{}, cascade.Newf(cascade.KindUnsupported, "pews: claim requires a journal store implementing CASAppender, got %T", js)
	}
	return cas.AppendIf(ctx, ticketID, len(entries), EventClaim, operationID, payload)
}
