// Purpose: the R-21.65/R-16.67 four-value node presence enum and the pure
//	R-21.197 hysteresis state machine that advances it from direct-probe
//	observations, plus the placement-scope rule (R-21.197/R-21.163) that
//	presence updates liveness and NEW placements only.
// Inputs: a DeviceRecord's persisted presence/counter fields
//	(records.go), a PresenceObservation from one probe attempt, and the
//	current instant from the injected Clock (prober.go never calls this
//	package's functions with time.Now() directly).
// Outputs: an updated DeviceRecord and whether a transition occurred, or
//	a typed fail-closed error for an unrecognized presence string.
// Constraints: R-21.65 fixes the enum at exactly four values with no
//	silent fifth default; R-21.197 requires three consecutive DIRECT
//	probe misses spanning >=30s to fall to unavailable and two
//	consecutive successes spanning >=60s to rise to reachable, so a
//	single probe never transitions presence in either direction;
//	unauthenticated or stale (past FreshnessWindow) evidence is refused
//	as evidence entirely, never counted as a miss or a hit.
// SPORT: internal/nodes Presence/ADDED, AdvancePresence/ADDED,
//	ResolveHeartbeatTimeoutPresence/ADDED (P1-E36-W7-S72-T2).

package nodes

import (
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// presenceParseError reports s as an unrecognized presence string —
// KindInvalidInput, never a silent fifth default.
func presenceParseError(s string) error {
	return cascade.Newf(cascade.KindInvalidInput, "nodes: %q is not a valid presence (want reachable, unavailable, remote-via-route or unknown)", s)
}

// Presence is the R-21.65/R-16.67 four-value node presence enum. The zero
// value is deliberately not a member, matching Liveness/Tier's identical
// "no permissive zero value" convention in this package.
type Presence string

const (
	// PresenceReachable reports two consecutive direct-probe successes
	// spanning at least HitDwellWindow.
	PresenceReachable Presence = "reachable"
	// PresenceUnavailable reports three consecutive direct-probe misses
	// spanning at least MissDwellWindow.
	PresenceUnavailable Presence = "unavailable"
	// PresenceRemoteViaRoute reports a direct probe failure resolved by
	// an injected RouteChecker (prober.go) as reachable only via a
	// configured route/tunnel. The default RouteChecker always reports
	// false, so this value is defined here and produced only once
	// S-72.T3's real travel-profile implementation is wired in.
	PresenceRemoteViaRoute Presence = "remote-via-route"
	// PresenceUnknown is the fail-closed default: no probe history yet,
	// or a heartbeat-stream timeout with no probe evidence in the
	// window at all (R-21.65's second, distinct signal from a direct
	// miss), or evidence older than FreshnessWindow.
	PresenceUnknown Presence = "unknown"
)

// Valid reports whether p is one of the four declared members.
func (p Presence) Valid() bool {
	switch p {
	case PresenceReachable, PresenceUnavailable, PresenceRemoteViaRoute, PresenceUnknown:
		return true
	}
	return false
}

// parsePresence parses s into a Presence. An unrecognized string is a
// typed cascade.KindInvalidInput error — never a silent fifth default —
// per this ticket's acceptance criteria. Package-private: no caller
// outside this package decodes a presence string yet — cmd/cascade/
// node_views.go only ever renders a Presence forward, via
// string(rec.Presence) (see newNodeRowView), never parses one back from
// a CLI flag or wire field, and the caller_site the stale
// testonly-allow.json entry named was speculative, not real (a genuine
// --presence filter flag would live in cmd/cascade/node.go's
// newNodeListCmd, outside this ticket's files_scope). Export it again,
// the one-line change, the day a real caller needs to decode a presence
// string from outside this package.
func parsePresence(s string) (Presence, error) {
	p := Presence(s)
	if !p.Valid() {
		return "", presenceParseError(s)
	}
	return p, nil
}

// The R-21.197 hysteresis constants.
const (
	// ProbeMissThreshold consecutive direct-probe misses transition
	// presence to unavailable.
	ProbeMissThreshold = 3
	// ProbeHitThreshold consecutive direct-probe successes transition
	// presence back to reachable.
	ProbeHitThreshold = 2
	// MissDwellWindow is the minimum span the ProbeMissThreshold misses
	// must cover before the transition fires.
	MissDwellWindow = 30 * time.Second
	// HitDwellWindow is the minimum span the ProbeHitThreshold successes
	// must cover before the transition fires.
	HitDwellWindow = 60 * time.Second
	// DefaultProbeInterval is the prober's tick period (R-16.37).
	DefaultProbeInterval = 30 * time.Second
	// FreshnessWindow bounds probe evidence: a result older than three
	// probe intervals is not evidence (R-21.163) and degrades presence
	// to unknown rather than being counted as a miss or a hit.
	FreshnessWindow = 3 * DefaultProbeInterval
)

// PresenceObservation is one direct-probe outcome fed into AdvancePresence.
type PresenceObservation struct {
	// OK is true for a probe success, false for a miss.
	OK bool
	// At is when the probe ran.
	At time.Time
	// Authenticated is true only for evidence carried over the enrolled
	// node channel. An unauthenticated LAN reply must never move
	// presence in either direction (R-21.163) — AdvancePresence refuses
	// it as evidence entirely rather than counting it as a miss.
	Authenticated bool
	// RouteOnly is true when the direct probe failed but an injected
	// RouteChecker resolved the node as reachable via a configured
	// route (prober.go).
	RouteOnly bool
}

// AdvancePresence applies obs to rec's persisted hysteresis state at
// instant now, returning the updated record and whether a transition
// occurred. Unauthenticated or stale-past-FreshnessWindow evidence is
// refused as evidence and never advances or resets a counter — a hostile
// or delayed reply must never be able to force or block a transition.
func AdvancePresence(rec DeviceRecord, obs PresenceObservation, now time.Time) (DeviceRecord, bool) {
	if !obs.Authenticated {
		return rec, false
	}
	if now.Sub(obs.At) > FreshnessWindow {
		return degradeToUnknown(rec, now)
	}
	if obs.RouteOnly {
		return transitionTo(rec, PresenceRemoteViaRoute, now)
	}
	if obs.OK {
		return advanceHit(rec, now)
	}
	return advanceMiss(rec, now)
}

func advanceHit(rec DeviceRecord, now time.Time) (DeviceRecord, bool) {
	rec.ConsecutiveMisses = 0
	rec.FirstMissAt = time.Time{}
	if rec.ConsecutiveHits == 0 {
		rec.FirstHitAt = now
	}
	rec.ConsecutiveHits++
	if rec.ConsecutiveHits < ProbeHitThreshold || now.Sub(rec.FirstHitAt) < HitDwellWindow {
		return rec, false
	}
	rec.ConsecutiveHits = 0
	rec.FirstHitAt = time.Time{}
	return transitionTo(rec, PresenceReachable, now)
}

func advanceMiss(rec DeviceRecord, now time.Time) (DeviceRecord, bool) {
	rec.ConsecutiveHits = 0
	rec.FirstHitAt = time.Time{}
	if rec.ConsecutiveMisses == 0 {
		rec.FirstMissAt = now
	}
	rec.ConsecutiveMisses++
	if rec.ConsecutiveMisses < ProbeMissThreshold || now.Sub(rec.FirstMissAt) < MissDwellWindow {
		return rec, false
	}
	rec.ConsecutiveMisses = 0
	rec.FirstMissAt = time.Time{}
	return transitionTo(rec, PresenceUnavailable, now)
}

// degradeToUnknown resets every hysteresis counter and moves rec to
// PresenceUnknown — used both for stale-past-FreshnessWindow evidence and
// the heartbeat-timeout-with-no-probe-evidence signal.
func degradeToUnknown(rec DeviceRecord, now time.Time) (DeviceRecord, bool) {
	rec.ConsecutiveMisses = 0
	rec.ConsecutiveHits = 0
	rec.FirstMissAt = time.Time{}
	rec.FirstHitAt = time.Time{}
	return transitionTo(rec, PresenceUnknown, now)
}

func transitionTo(rec DeviceRecord, next Presence, now time.Time) (DeviceRecord, bool) {
	transitioned := rec.Presence != next
	rec.Presence = next
	if transitioned {
		rec.PresenceChangedAt = now
	}
	return rec, transitioned
}

// ResolveHeartbeatTimeoutPresence applies R-21.65's second signal: a
// Q/S-36.T2 heartbeat-stream timeout with NO probe evidence in the
// window resolves presence to unknown — distinct from a direct-probe-miss
// timeout, which resolves to unavailable via AdvancePresence/advanceMiss
// instead. "No probe evidence in the window" means rec has no in-flight
// miss/hit streak newer than timeout; a record already mid-hysteresis
// from real direct-probe evidence is left alone; the caller (prober.go)
// only calls this leg when its own probe attempt found nothing to report.
func ResolveHeartbeatTimeoutPresence(rec DeviceRecord, now time.Time, timeout time.Duration) (DeviceRecord, bool) {
	if ComputeLiveness(rec, now, timeout) != LivenessUnknown {
		return rec, false
	}
	if !rec.FirstMissAt.IsZero() && now.Sub(rec.FirstMissAt) <= timeout {
		return rec, false
	}
	if !rec.FirstHitAt.IsZero() && now.Sub(rec.FirstHitAt) <= timeout {
		return rec, false
	}
	return degradeToUnknown(rec, now)
}

// R-21.197's placement-scope rule (a presence transition updates liveness
// and eligibility for NEW placement decisions ONLY) has no runtime gate
// in this package to anchor — no scheduler exists in this tree yet
// (S-37.T1's forward ticket) — so it is documented here rather than as a
// dead exported marker constant. ScopeRelease/CancelInFlight below are
// the release-side half of the same rule: an in-flight job's own
// cancellation path, never a presence transition, is what may ever
// release its scope.

// ScopeRelease releases one piece of state an in-flight job held: a W9
// atomic reservation, an AC/S-59 lease, or its worktree. Each concrete
// release is owned by the ticket that built the corresponding resource;
// this package only defines the seam a stall/cancellation path composes
// them through.
type ScopeRelease func() error

// CancelInFlight runs every release in releases, in order, continuing
// past a failing one (a partial release must never leave the others
// silently un-released) and returns the first error encountered, if any.
// This is the "one transaction" release contract R-21.197 requires when a
// job whose node went unavailable is canceled — it never fires on a mere
// presence flap, only on an explicit cancellation decision the caller
// (AH/S-70.T3's stall path, or its own future presence-aware caller)
// makes.
func CancelInFlight(releases ...ScopeRelease) error {
	var first error
	for _, release := range releases {
		if release == nil {
			continue
		}
		if err := release(); err != nil && first == nil {
			first = err
		}
	}
	return first
}
