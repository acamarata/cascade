// Purpose: R-21.225's three-state liveness result and the derivation
//
//	function every heartbeat consumer (doctor.go's health check, and
//	later S-37.T1 placement / S-37.T3 re-queue) reads.
//
// Inputs: a DeviceRecord's persisted LastSeen timestamp (records.go,
//
//	S-36.T1), the current instant from the injected Clock, and a
//	heartbeat timeout window.
//
// Outputs: a Liveness value. Unknown is the fail-closed default: a node
//
//	that has never heartbeated, or whose most recent heartbeat is older
//	than the timeout, is Unknown, never silently treated as reachable.
//
// Constraints: R-21.225 — Liveness is a THREE-STATE result {reachable,
//
//	unavailable, unknown}, with unknown the fail-closed default on
//	heartbeat timeout. This ticket derives Liveness from the persisted
//	LastSeen field LIVE, at read time, rather than adding a fourth
//	persisted enum field to DeviceRecord: records.go is S-36.T1's file
//	and is NOT in this ticket's files_scope (add/change), so no new
//	struct field can land there. A derived value is also the more
//	correct shape for "unknown is the fail-closed default on timeout" —
//	a persisted enum would need an active re-computation pass to age out
//	on its own, where a derived read never goes stale. See the ticket
//	journal's CONTRADICTIONS section for the full quote of both sides.
//	AJ/S-72.T2 (W7) extends this same field with a `remote-via-route`
//	value; this ticket defines the field and its three values only.
//
// SPORT: internal/nodes Liveness/ADDED (P1-E17-W4-S36-T2).

package nodes

import "time"

// Liveness is a node's three-state liveness result (R-21.225). The zero
// value is LivenessUnknown, matching this package's established
// fail-closed convention (Tier's zero value is likewise invalid-by-design;
// see trust.go).
type Liveness string

const (
	// LivenessUnknown is the fail-closed default: no heartbeat has ever
	// been recorded, or the most recent one is older than the configured
	// timeout. S-37.T1 treats anything other than LivenessReachable as
	// not placeable; S-37.T3 treats it as a re-queue signal.
	LivenessUnknown Liveness = "unknown"
	// LivenessReachable reports a heartbeat inside the timeout window,
	// signed and verified.
	LivenessReachable Liveness = "reachable"
	// LivenessUnavailable reports an explicit negative signal (a
	// controller-observed dispatch failure, S-37.T2/S-37.T3's concern) —
	// distinct from "no data yet" (LivenessUnknown). This ticket defines
	// the value; nothing in this ticket's own code path produces it,
	// since dispatch failure detection belongs to S-37.T2/S-37.T3.
	LivenessUnavailable Liveness = "unavailable"
)

// DefaultHeartbeatTimeout is the window since LastSeen within which a
// node is considered reachable. Three times DefaultHeartbeatInterval
// (heartbeat.go), so two consecutive missed heartbeats are tolerated
// before liveness degrades — a single delayed tick (scheduler jitter,
// a slow controller) must not flip a healthy node to unknown.
const DefaultHeartbeatTimeout = 3 * DefaultHeartbeatInterval

// ComputeLiveness derives rec's current Liveness at instant now, using
// timeout as the heartbeat freshness window. A zero LastSeen (never
// heartbeated) and a LastSeen older than timeout both resolve to
// LivenessUnknown — the fail-closed default; only a LastSeen strictly
// within the window resolves to LivenessReachable.
func ComputeLiveness(rec DeviceRecord, now time.Time, timeout time.Duration) Liveness {
	if rec.LastSeen.IsZero() {
		return LivenessUnknown
	}
	if timeout <= 0 {
		return LivenessUnknown
	}
	age := now.Sub(rec.LastSeen)
	if age < 0 || age > timeout {
		return LivenessUnknown
	}
	return LivenessReachable
}

// GetLiveness reads nodeID's current DeviceRecord from records and
// returns its derived Liveness at clock's current instant. Returns
// LivenessUnknown alongside the store's error for any node this store
// cannot positively resolve (unenrolled or unreadable), per this
// package's fail-closed convention: a caller that ignores the error must
// still see the safe value, never a zero string coerced into something
// that looks like success.
func GetLiveness(records *RecordStore, clock Clock, nodeID string, timeout time.Duration) (Liveness, error) {
	rec, err := records.Get(nodeID)
	if err != nil {
		return LivenessUnknown, err
	}
	return ComputeLiveness(rec, clock.Now(), timeout), nil
}
