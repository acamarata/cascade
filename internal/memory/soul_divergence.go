package memory

// Purpose: the divergence vocabulary — what a reconcile-on-load found,
//   the typed event a conflict emits, and the sink it is reported to.
//   Split from soul.go for the 300-line file cap; the behaviour that
//   produces these values lives in soul_reconcile.go.
// Inputs: none; these are value types.
// Outputs: values that describe a divergence.
// Constraints: THE RULE OF THIS FILE — nothing declared here may carry
//   soul text. A divergence report travels to an event bus and into a
//   diagnostic note, both read by things with no business seeing the
//   user's identity document, so every field is a version, a digest or an
//   instant.
// SPORT: G/memory-soul-store (ADD, placeholder per T-2 sport_updates).

import (
	"context"
	"time"
)

// DivergenceOutcome is what a reconcile-on-load found.
type DivergenceOutcome string

// The three outcomes, which are exhaustive.
const (
	// DivergenceNone means the file and the store agree.
	DivergenceNone DivergenceOutcome = "none"
	// DivergenceReconciled means the file had moved on alone and its
	// content was adopted through the audited path.
	DivergenceReconciled DivergenceOutcome = "reconciled"
	// DivergenceConflict means both sides changed since the last
	// reconcile. Nothing is merged, adopted or discarded.
	DivergenceConflict DivergenceOutcome = "conflict"
)

// DivergenceResult reports one reconcile-on-load.
//
// Like AuditEntry it names hashes and versions and never content: a
// divergence report travels to an event bus and into a diagnostic note,
// both of which are read by things that have no business seeing the SOUL.
type DivergenceResult struct {
	// Outcome is what was found.
	Outcome DivergenceOutcome `json:"outcome"`
	// Version is the store's version after the call.
	Version int `json:"version"`
	// LastReconciledVersion is the version as of the last reconcile.
	LastReconciledVersion int `json:"last_reconciled_version"`
	// StoredHash is the digest the store recorded at its last write.
	StoredHash string `json:"stored_hash"`
	// FileHash is the digest of the body currently on disk, empty when
	// no file is there.
	FileHash string `json:"file_hash"`
}

// SoulDivergedEvent is the event-bus name of a conflict.
const SoulDivergedEvent = "memory.soul.diverged"

// DivergenceEvent is the typed event emitted on a conflict. Its fields are
// the DivergenceResult's, plus the instant, and no more.
type DivergenceEvent struct {
	// Version and LastReconciledVersion locate the conflict in the
	// version history.
	Version               int `json:"version"`
	LastReconciledVersion int `json:"last_reconciled_version"`
	// StoredHash and FileHash identify the two sides without carrying
	// either one's text.
	StoredHash string `json:"stored_hash"`
	FileHash   string `json:"file_hash"`
	// DetectedAt is the instant of detection, from the injected clock.
	DetectedAt time.Time `json:"detected_at"`
}

// EventName returns the bus name of this event.
func (DivergenceEvent) EventName() string { return SoulDivergedEvent }

// SoulDivergenceSink receives conflict notifications. A store built with
// no sink discards them, which is a documented configuration rather than
// a nil-pointer hazard, and never changes what is stored.
type SoulDivergenceSink interface {
	// SoulDiverged reports one unresolved conflict.
	SoulDiverged(ctx context.Context, ev DivergenceEvent) error
}

// discardSoulEvents is the no-sink default.
type discardSoulEvents struct{}

// SoulDiverged discards the event.
func (discardSoulEvents) SoulDiverged(context.Context, DivergenceEvent) error { return nil }
