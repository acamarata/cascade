package learn

// Purpose: the six learn event kinds published on the events bus.
// Inputs: none.
// Outputs: the EventKind constants and EventKinds.
// Constraints: every kind sits in the "learn." namespace; EventKinds
//   returns a fresh slice in declaration order, so a caller cannot change
//   the set by mutating the result.
// SPORT: internal.learn.EventKinds/ADDED (P1-LRN-03).

import "github.com/acamarata/cascade/internal/events"

// LearnEventNamespace is the prefix (before the ".") of every learn event
// kind.
const LearnEventNamespace = "learn"

// The six learn event kinds.
const (
	// EventLearnApply: a learned change was applied to the config file.
	EventLearnApply events.EventKind = "learn.apply"
	// EventLearnHeld: a learned change was held (a TierDiff hold reason).
	EventLearnHeld events.EventKind = "learn.held"
	// EventLearnVersion: a new learned-config version was recorded.
	EventLearnVersion events.EventKind = "learn.version"
	// EventLearnRevert: a learned config was reverted to a prior version.
	EventLearnRevert events.EventKind = "learn.revert"
	// EventLearnTierMismatch: a stored tier disagrees with the computed one.
	EventLearnTierMismatch events.EventKind = "learn.tier_mismatch"
	// EventLearnFallback: a learned value fell back to the static default.
	EventLearnFallback events.EventKind = "learn.fallback"
)

// EventKinds returns a fresh slice of the six learn event kinds in
// declaration order.
func EventKinds() []events.EventKind {
	return []events.EventKind{
		EventLearnApply, EventLearnHeld, EventLearnVersion,
		EventLearnRevert, EventLearnTierMismatch, EventLearnFallback,
	}
}
