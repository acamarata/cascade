// Purpose (this file): the pure slot-construction and TTL-expiry helpers
// compositor.go's Update*/Snapshot methods call. Split out to keep
// compositor.go under the repo's 300-line file cap (12-QUALITY-
// CONSTITUTION.md Art.10) - a fifth file beyond the ticket's suggested
// four-file split (snapshot/compositor/diff/rpc), which this ticket's own
// files_scope.add did not name. Recorded as a deviation, not silent: the
// repo-wide 300-line gate (AGENT-BRIEF.md's REPO-WIDE GATES) is a harder,
// more general constraint than one ticket's suggested split, and no other
// agent's files_scope collides with a new file inside this ticket's own
// package.
//
// Inputs: registry.ProviderRecord/LaneRecord, nodes.DeviceRecord, the
// current instant, and TTL durations.
// Outputs: ProviderSlot / NodeSlot values.
// Constraints: pure functions, no I/O, no bare time.Now - every instant is
// a parameter. buildNodeSlot no longer takes a heartbeat timeout or a
// probe-miss count (P1-E36-W7-S72-T2's presence migration): Presence
// comes verbatim from DeviceRecord.Presence, the one field nodes.Prober
// writes, never re-derived here.
//
// SPORT: fleet.capacity.snapshot (compositor helpers, ADD, per T-1
// sport_updates; buildNodeSlot simplified, P1-E36-W7-S72-T2).

package capacity

import (
	"time"

	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/providers/registry"
)

// bucketRank orders State by severity for worst-case aggregation
// across multiple lanes sharing one (provider, bucket) pair: the most
// actionable/severe state wins. Documented, explicit choice (not a
// borrowed comparison from elsewhere): auth-required needs operator
// action and outranks exhausted (temporary); unknown is more concerning
// than a confirmed-degraded-but-usable constrained state, but less than a
// confirmed failure.
var bucketRank = map[State]int{
	StateAvailable:    0,
	StateUnknown:      1,
	StateConstrained:  2,
	StateExhausted:    3,
	StateAuthRequired: 4,
}

// worstState returns the highest-ranked (most severe) state among states,
// or StateUnknown for an empty input (no lane data at all for this
// bucket - the "source absent" case, never a silent StateAvailable).
func worstState(states []State) State {
	if len(states) == 0 {
		return StateUnknown
	}
	worst := states[0]
	for _, s := range states[1:] {
		if bucketRank[s] > bucketRank[worst] {
			worst = s
		}
	}
	return worst
}

// bucketOrder is the canonical bucket order buildProviderSlot walks, and the
// order the widget breaks ties in.
var bucketOrder = []registry.CapacityBucket{BucketInteractiveUsage, BucketAgentSDKCredit, BucketAPICredit}

// buildProviderSlot composes rec's ProviderSlot from its own lanes only
// (never another provider's data - the ticket's "derive each figure from
// its own source of truth" rule). Each of the three canonical BucketKinds
// gets its own worst-case-aggregated State and its own reset estimate; a
// bucket with zero matching lanes is reported StateUnknown in Buckets (the
// map keeps all three keys) but takes no part in the provider's aggregate:
// the slot's State is the worst state over the NON-EMPTY buckets, and
// StateUnknown only when the provider has no lane at all (see the note below).
//
// ROOT CAUSE NOTE (R9 F-3, C14). The first version took
// worstState over all three buckets, so an empty bucket (unknown, rank 1)
// outranked available (rank 0) and every healthy one-lane provider read
// "unknown". It also kept ONE running reset estimate across the bucket loop
// and copied it into both windows of every bucket, so a bucket's windows
// carried another bucket's estimate and a five-hour estimate was shown as a
// seven-day reset (a fabricated figure). Now the estimate is per bucket,
// comes only from the lanes in that bucket's worst state, and lands in the
// five-hour window alone; the seven-day window has no reset source yet
// (P1-TOP-13), so its ResetsIn stays zero.
//
// UpdatedAt is the instant of this read. A caller that keeps the slot when a
// later read fails therefore keeps an aging UpdatedAt (compositor.go).
func buildProviderSlot(rec registry.ProviderRecord, lanes []registry.LaneRecord, now time.Time) ProviderSlot {
	byBucket := map[registry.CapacityBucket][]registry.LaneRecord{}
	for _, l := range lanes {
		byBucket[l.Capacity] = append(byBucket[l.Capacity], l)
	}

	buckets := make(map[BucketKind]Bucket, len(bucketOrder))
	var reauth bool
	var slotReset time.Time
	var populated []State
	for _, kind := range bucketOrder {
		bucketLanes := byBucket[kind]
		b, reset := buildBucket(bucketLanes, now)
		buckets[kind] = b
		if len(bucketLanes) == 0 {
			continue
		}
		populated = append(populated, b.State)
		reauth = reauth || b.State == StateAuthRequired
		if reset.After(slotReset) {
			slotReset = reset
		}
	}

	return ProviderSlot{
		ProfileRef:     rec.Name,
		Buckets:        buckets,
		State:          worstState(populated),
		ResetEstimate:  slotReset,
		ReauthRequired: reauth,
		UpdatedAt:      now,
	}
}

// buildBucket aggregates one bucket's lanes: its worst State and the latest
// ResetEstimate among the lanes in that worst state (a reset on a lane that
// is not what degrades the bucket says nothing about when the bucket
// recovers). No lanes yields StateUnknown and the zero time.
func buildBucket(lanes []registry.LaneRecord, now time.Time) (Bucket, time.Time) {
	states := make([]State, 0, len(lanes))
	for _, l := range lanes {
		states = append(states, l.State)
	}
	worst := worstState(states)
	var reset time.Time
	for _, l := range lanes {
		if l.State == worst && l.ResetEstimate.After(reset) {
			reset = l.ResetEstimate
		}
	}
	return Bucket{
		State:    worst,
		FiveHour: Window{UtilizationPct: WindowUtilizationUnknown, ResetsIn: resetsIn(reset, now)},
		SevenDay: Window{UtilizationPct: WindowUtilizationUnknown},
	}, reset
}

// resetsIn returns max(0, at-now) - never a negative duration for an
// already-past estimate.
func resetsIn(at, now time.Time) time.Duration {
	if at.IsZero() || !at.After(now) {
		return 0
	}
	return at.Sub(now)
}

// buildNodeSlot composes rec's NodeSlot. Presence is read verbatim from
// rec's own persisted field - nodes.Prober/nodes.AdvancePresence
// (P1-E36-W7-S72-T2) are the only writers of that field, so this
// function never re-derives it. A record predating that ticket (an empty
// Presence, never yet touched by the real prober) reports PresenceUnknown
// - the fail-closed default, never a silent PresenceReachable.
func buildNodeSlot(rec nodes.DeviceRecord, now time.Time) NodeSlot {
	presence := rec.Presence
	if !presence.Valid() {
		presence = PresenceUnknown
	}
	return NodeSlot{
		ID:         rec.NodeID,
		Presence:   presence,
		TrustTier:  string(rec.Tier),
		UpdatedAt:  now,
		Diagnostic: hardwareUnavailableDiagnostic,
	}
}

// expireProviderSlot re-checks slot's staleness against now/ttl: past ttl,
// every bucket and the slot's own State fall to StateUnknown rather than
// silently retaining the last-good value.
func expireProviderSlot(slot ProviderSlot, now time.Time, ttl time.Duration) ProviderSlot {
	if now.Sub(slot.UpdatedAt) <= ttl {
		return slot
	}
	expired := make(map[BucketKind]Bucket, len(slot.Buckets))
	for kind, b := range slot.Buckets {
		b.State = StateUnknown
		expired[kind] = b
	}
	slot.Buckets = expired
	slot.State = StateUnknown
	return slot
}

// expireNodeSlot re-checks slot's staleness against now/ttl: past ttl,
// Presence falls to PresenceUnknown - the fail-closed default a caller
// cannot distinguish from "no data yet" is exactly the point (task-spec
// error path).
func expireNodeSlot(slot NodeSlot, now time.Time, ttl time.Duration) NodeSlot {
	if now.Sub(slot.UpdatedAt) <= ttl {
		return slot
	}
	slot.Presence = PresenceUnknown
	return slot
}
