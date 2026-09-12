// Purpose (this file): the R-21.166 probe-only admission for an `unknown`
// slot and the R-21.109 read-time staleness degrade. The slot-state enum
// itself is NOT redeclared here: State (snapshot.go, this package) already
// re-exports registry.LaneState's exact five members {available,
// constrained, exhausted, auth-required, unknown}, matching this ticket's
// task-1 request verbatim -- see snapshot.go's own CONTRACT DEVIATION note
// for the identical precedent.
//
// Inputs: a profile id, the current State, its observation source and
// timestamp, and the injected Clock.
// Outputs: the DefaultProbeAdmission ProbeAdmission implementation, and
// DegradeStaleness's degraded State.
// Constraints: at most one in-flight probe per profile; a profile inside
// its backoff window is exhausted regardless of its raw observed state;
// no bare time.Now (Clock is injected).
//
// SPORT: fleet.capacity.probe (ADD, P1-E31-W6-S63-T2).

package capacity

import (
	"sync"
	"time"
)

// ObservationSource is the R-21.109 per-source freshness classification a
// capacity observation carries.
type ObservationSource string

// The three closed ObservationSource members.
const (
	SourceProviderStatus ObservationSource = "provider-status"
	SourceCLIObservation ObservationSource = "cli-observation"
	SourceUserEstimate   ObservationSource = "user-estimate"
)

// StalenessTTL returns src's R-21.109 freshness expiry. An unknown or
// unset source returns zero -- the most restrictive value, since a zero
// TTL degrades on the very next read (fail-closed per §5.15).
func StalenessTTL(src ObservationSource) time.Duration {
	switch src {
	case SourceProviderStatus:
		return 10 * time.Minute
	case SourceCLIObservation:
		return 30 * time.Minute
	case SourceUserEstimate:
		return 24 * time.Hour
	default:
		return 0
	}
}

// DegradeStaleness applies R-21.109's read-time rule: an `available`
// observation older than its source's staleness_ttl degrades to
// `unknown`. Every other State is returned unchanged -- the rule names
// only the available -> unknown direction.
func DegradeStaleness(state State, src ObservationSource, observedAt, now time.Time) State {
	if state != StateAvailable {
		return state
	}
	if now.Sub(observedAt) > StalenessTTL(src) {
		return StateUnknown
	}
	return state
}

// Clock is this package's own wall-clock seam (compositor.go, S-63.T1) --
// not redeclared here.

// probeBackoffLadder is the R-21.166 doubling backoff schedule, capped at
// one hour.
var probeBackoffLadder = []time.Duration{
	time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute,
	16 * time.Minute, 32 * time.Minute, time.Hour,
}

// probeRecord is one profile's backoff bookkeeping.
type probeRecord struct {
	inFlight       bool
	classification State
	backoffUntil   time.Time
	step           int
}

// DefaultProbeAdmission is this ticket's production ProbeAdmission
// implementation: at most one in-flight probe per profile, and the
// exponential backoff a probe failure opens.
type DefaultProbeAdmission struct {
	mu      sync.Mutex
	records map[string]*probeRecord
}

// NewDefaultProbeAdmission returns a ready-to-use DefaultProbeAdmission.
func NewDefaultProbeAdmission() *DefaultProbeAdmission {
	return &DefaultProbeAdmission{records: make(map[string]*probeRecord)}
}

// TryAdmit reports whether profile may be scheduled right now: false when
// profile sits inside its backoff window. When dispatchProbe is true (the
// caller is about to treat an `unknown` slot as available via a probe)
// and profile is not in backoff, TryAdmit additionally checks that no
// other probe is already in flight for profile; if admitted, exactly one
// in-flight probe is now recorded until Classify resolves it.
func (a *DefaultProbeAdmission) TryAdmit(profile string, dispatchProbe bool, now time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	rec := a.records[profile]
	if rec != nil && now.Before(rec.backoffUntil) {
		return false
	}
	if !dispatchProbe {
		return true
	}
	if rec != nil && rec.inFlight {
		return false
	}
	if rec == nil {
		rec = &probeRecord{}
		a.records[profile] = rec
	}
	rec.inFlight = true
	return true
}

// Classify records a probe's outcome for profile. StateAvailable or
// StateConstrained means the probe succeeded: the backoff clears
// entirely. Any other outcome (StateExhausted, StateAuthRequired, or an
// unrecognized value) is a failure classified to StateExhausted or
// StateAuthRequired per outcome, opening or advancing the doubling
// backoff -- an unrecognized outcome fails closed to StateExhausted
// rather than being silently ignored.
func (a *DefaultProbeAdmission) Classify(profile string, outcome State, now time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	rec := a.records[profile]
	if rec == nil {
		rec = &probeRecord{}
		a.records[profile] = rec
	}
	rec.inFlight = false
	switch outcome {
	case StateAvailable, StateConstrained:
		delete(a.records, profile)
		return
	case StateAuthRequired:
		rec.classification = StateAuthRequired
	default:
		rec.classification = StateExhausted
	}
	step := rec.step
	if step >= len(probeBackoffLadder) {
		step = len(probeBackoffLadder) - 1
	}
	rec.backoffUntil = now.Add(probeBackoffLadder[step])
	if rec.step < len(probeBackoffLadder)-1 {
		rec.step++
	}
}
