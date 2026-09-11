package supervision

// Purpose (this file): ProgressTracker — the per-session last-progress
// timestamp, the per-job gate-denied window (R-16.73's 3-in-30-minute
// rule), and the tri-state ProgressStatus this ticket's discipline point
// requires: "not seen recently" (ProgressStalled, a real conclusion from
// known data) and "could not tell" (ProgressUnknown, the source itself is
// unavailable) are different answers, never conflated and never a silent
// ProgressHealthy.
//
// Inputs: Touch (a progress observation), MarkSourceUnavailable/Available
// (the bus subscription's own liveness), RecordGateDenied (a gate-denied
// signal), and a Clock/threshold for elapsed-time classification.
// Outputs: Status/Confidence read the tracker's current conclusion.
// Constraints: mutex-protected in-memory state only (no persistence
// requirement in this ticket's contract); no bare time.Now — every
// timestamp is read from the injected runtime.Clock.
//
// SPORT: fleet.supervision.stall/ADDED (P1-E18-W4-S39-T5).

import (
	"context"
	"sync"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
)

// gateDeniedWindow is the R-16.73 counting window: three gate-denied
// signals for the SAME job within this window fire the gate-denied
// stall kind.
const gateDeniedWindow = 30 * time.Minute

// gateDeniedFireCount is the R-16.73 counting threshold.
const gateDeniedFireCount = 3

// ProgressStatus is the tracker's tri-state conclusion about one session.
type ProgressStatus int

const (
	// ProgressUnknown reports that the tracker cannot say: either the
	// session has never been observed, or the event source is currently
	// marked unavailable. "Could not tell" is never reported as
	// ProgressHealthy.
	ProgressUnknown ProgressStatus = iota
	// ProgressHealthy reports a session observed within threshold.
	ProgressHealthy
	// ProgressStalled reports a session whose last observed progress is
	// older than threshold — a real, known conclusion ("not seen
	// recently"), distinct from ProgressUnknown.
	ProgressStalled
)

// String returns status's stable lowercase name.
func (s ProgressStatus) String() string {
	switch s {
	case ProgressUnknown:
		return "unknown"
	case ProgressHealthy:
		return "healthy"
	case ProgressStalled:
		return "stalled"
	default:
		return "unknown"
	}
}

// ProgressTracker records per-session last-progress timestamps and
// per-job gate-denied windows, and answers "is this session stalled"
// fail-closed. The zero value is not usable; construct with
// NewProgressTracker.
type ProgressTracker struct {
	clock     runtime.Clock
	threshold time.Duration

	mu            sync.Mutex
	lastSeen      map[string]int64   // sessionID -> unix millis
	gateDenials   map[string][]int64 // jobID -> unix millis, pruned to gateDeniedWindow
	sourceHealthy bool
}

// NewProgressTracker builds a ProgressTracker. threshold must be
// positive; clock must be non-nil (tests inject a *runtime.FixedClock).
func NewProgressTracker(clock runtime.Clock, threshold time.Duration) *ProgressTracker {
	return &ProgressTracker{
		clock:         clock,
		threshold:     threshold,
		lastSeen:      make(map[string]int64),
		gateDenials:   make(map[string][]int64),
		sourceHealthy: true,
	}
}

// Touch records sessionID as having made progress at the tracker's
// current clock instant.
func (t *ProgressTracker) Touch(sessionID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lastSeen[sessionID] = t.clock.Now().UnixMilli()
}

// MarkSourceUnavailable reports that this tracker's event source (the
// fleet.sessions.changed subscription) is no longer delivering. Every
// Status/Confidence call reports ProgressUnknown until MarkSourceAvailable.
func (t *ProgressTracker) MarkSourceUnavailable() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sourceHealthy = false
}

// MarkSourceAvailable reports that the event source is delivering again.
func (t *ProgressTracker) MarkSourceAvailable() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sourceHealthy = true
}

// Status reports sessionID's current tri-state conclusion, and the
// instant progress was last observed (0 when the status is
// ProgressUnknown — there is nothing to report a "since" from).
func (t *ProgressTracker) Status(sessionID string) (ProgressStatus, int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.sourceHealthy {
		return ProgressUnknown, 0
	}
	last, ok := t.lastSeen[sessionID]
	if !ok {
		return ProgressUnknown, 0
	}
	now := t.clock.Now().UnixMilli()
	if time.Duration(now-last)*time.Millisecond > t.threshold {
		return ProgressStalled, last
	}
	return ProgressHealthy, last
}

// setThreshold updates the idle/stall cutoff under lock.
func (t *ProgressTracker) setThreshold(threshold time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.threshold = threshold
}

// Watched reports every sessionID this tracker has ever Touch'd, for
// Detector.Poll to iterate.
func (t *ProgressTracker) Watched() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]string, 0, len(t.lastSeen))
	for id := range t.lastSeen {
		out = append(out, id)
	}
	return out
}

// Confidence implements governor.ConfidenceProvider: ProgressHealthy
// reports full confidence (not stuck), ProgressStalled reports zero
// confidence (stuck), and ProgressUnknown — "could not tell" — returns
// ErrSourceUnavailable rather than guessing either way. Advance's own
// isBelowThreshold treats a ConfidenceProvider error as "stuck" (fail
// closed), so an unavailable source still escalates; it never silently
// skips escalation, it only reports a DIFFERENT stall kind for it (see
// stall.go's classification, which is what actually distinguishes
// "known idle" from "could not tell" for observability).
func (t *ProgressTracker) Confidence(_ context.Context, sessionID string) (float64, error) {
	switch status, _ := t.Status(sessionID); status {
	case ProgressHealthy:
		return 1, nil
	case ProgressStalled:
		return 0, nil
	case ProgressUnknown:
		return 0, ErrSourceUnavailable
	default:
		return 0, ErrSourceUnavailable
	}
}

// RecordGateDenied appends a gate-denied observation for jobID at
// instant at (unix millis), prunes every entry older than
// at-gateDeniedWindow, and reports whether the pruned count has now
// reached gateDeniedFireCount (R-16.73's 3-in-30-minute rule). Firing
// does not reset the window: a fourth denial within the same window
// fires again on every call once the threshold is met, which
// stall.go's own escalation-idempotency (via EscalationLadder.Advance's
// per-entity terminal guard) is what actually prevents from producing
// runaway escalations, not this counter.
func (t *ProgressTracker) RecordGateDenied(jobID string, at int64) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	cutoff := at - gateDeniedWindow.Milliseconds()
	kept := t.gateDenials[jobID][:0]
	for _, ts := range t.gateDenials[jobID] {
		if ts > cutoff {
			kept = append(kept, ts)
		}
	}
	kept = append(kept, at)
	t.gateDenials[jobID] = kept
	return len(kept) >= gateDeniedFireCount
}
