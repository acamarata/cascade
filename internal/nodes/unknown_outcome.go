package nodes

import (
	"context"
	"fmt"
)

// Purpose (this file): the HELD half of remote-dispatch failure semantics
//
//	— what happens to work whose effect nobody can establish.
//
// WHY THIS IS NOT A RE-QUEUE. When a node is lost mid-dispatch the
//
//	controller knows the work was shipped and does not know whether it ran.
//	For an action declared idempotent that does not matter: running it twice
//	is indistinguishable from running it once, so it re-queues. For one that
//	is not, both choices are destructive — re-running may duplicate an
//	external effect, abandoning may lose work that succeeded — and nothing
//	in the system can tell which. So it is HELD for a person, and the hold
//	is a first-class state rather than a dropped dispatch (R-21.221).
//
// AND IT IS NEVER SILENT. A held item that nobody is told about is a lost
//
//	one with extra steps. Holding therefore REQUIRES a filer: with none
//	wired, HoldUnknownOutcome refuses rather than holding quietly, because
//	the alternative is a controller that reports work as handled while it
//	sits somewhere nobody looks.
//
// Inputs: the lost dispatch's identity and the loss signal.
// Outputs: an attention entry, or a typed refusal.
// SPORT: internal/nodes:unknown-outcome (ADD) — P1-E17-W4-S37-T3.

// HeldOutcome is one dispatch held for human disposition.
type HeldOutcome struct {
	// DispatchID names the dispatch whose outcome is unknown.
	DispatchID string
	// ActionID is the action that may or may not have run. It is the
	// stable id the node's durable dedup log keys on, so whoever
	// reconciles this can ask the node directly.
	ActionID string
	// NodeID is the node the work was shipped to.
	NodeID string
	// Attempt is the fencing number the lost attempt ran under.
	Attempt uint64
	// Signal is what told the controller the node was lost.
	Signal LossSignal
	// Reason is one sentence a person can act on.
	Reason string
}

// AttentionFiler surfaces a held outcome where a person will see it.
//
// A narrow interface rather than the attention store itself, matching this
// package's other seams: internal/nodes owns the DECISION to hold and the
// composition root owns where the entry lands.
type AttentionFiler interface {
	FileUnknownOutcome(ctx context.Context, held HeldOutcome) error
}

// HoldUnknownOutcome records held and returns the typed refusal that stops
// the caller proceeding as though the dispatch were finished.
//
// The error is always non-nil on success too, and that is the point: the
// caller must not be able to treat a held dispatch as a completed one. A
// filer that fails is reported INSTEAD, because a hold nobody was told
// about is the worse of the two failures.
func HoldUnknownOutcome(ctx context.Context, filer AttentionFiler, held HeldOutcome) error {
	if held.DispatchID == "" || held.ActionID == "" {
		return errUnidentifiedHold()
	}
	if filer == nil {
		return errNoAttentionFiler(held.DispatchID)
	}
	if err := filer.FileUnknownOutcome(ctx, held); err != nil {
		return fmt.Errorf("nodes: dispatch %s could not be held for review: %w", held.DispatchID, err)
	}
	return ErrUnknownOutcome(held.DispatchID, held.ActionID)
}

// heldReason renders the sentence an operator reads on the attention entry.
func heldReason(signal LossSignal, nodeID string) string {
	return "the node " + nodeID + " was lost (" + string(signal) +
		") after the work was shipped, and the action is not declared idempotent: " +
		"re-running it may duplicate its effect and abandoning it may lose work that succeeded"
}
