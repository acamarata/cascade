package nodes

// Purpose: the fleet-state half of the placement decision — whether a node
//
//	is, right now, in a state that can accept new work.
//
// Inputs: an enrolled node's record (presence, drained flag) and the
//
//	engine's live tunnel-state lookup.
//
// Outputs: an exclusion reason, or nothing when the node's state clears.
// Constraints: every check fails closed. An unknown presence, an absent
//
//	connection lookup and an unrecognized tunnel state all exclude.
//
// SPORT: internal/nodes placement fleet-state filters (ADD) — P1-E17-W4-S37-T1.

// excludedByFleetState reports whether the node's current state prevents
// placement, checked in the order an operator would act on: drained first
// (a deliberate operator decision), then presence, then connectivity.
func (e Engine) excludedByFleetState(rec DeviceRecord) (reason ExclusionReason, detail string, excluded bool) {
	if rec.Drained {
		return ReasonDrained, "node is drained and is not accepting new work", true
	}
	if !placeablePresence(rec.Presence) {
		return ReasonNotReachable, "presence is " + presenceName(rec.Presence) + ", not reachable", true
	}
	if state, ok := e.tunnelState(rec.NodeID); !ok || state != TunnelUp {
		return ReasonNotConnected, "tunnel is " + tunnelStateName(state, ok) + ", not up", true
	}
	return "", "", false
}

// placeablePresence reports whether a presence value permits placement.
//
// ONLY PresenceReachable does. This is an allow-list rather than a
// "not unavailable" test, and the difference is the whole point:
//
//   - PresenceUnknown is the fail-closed default after a heartbeat timeout.
//     Treating it as placeable would send work to a node nothing has heard
//     from, which is precisely the case the three-state model was introduced
//     to stop collapsing into "probably fine".
//   - PresenceRemoteViaRoute is a real, reachable-over-a-fallback-route
//     state that a later ticket (AJ/S-72.T2) owns for its own purposes. It
//     is deliberately NOT read as placeable here: this ticket's contract
//     says that value is not consulted, and an allow-list means a value
//     added to the enum later cannot silently become placeable by default.
func placeablePresence(p Presence) bool { return p == PresenceReachable }

// presenceName renders a presence for an error detail, naming the empty
// value explicitly — a node the prober has never visited carries "" and
// "presence is , not reachable" would read as a bug in the message.
func presenceName(p Presence) string {
	if p == "" {
		return "(never probed)"
	}
	return string(p)
}

// tunnelState consults the engine's lookup. ok is false when no lookup is
// wired, which reports NO node as connected: an engine built without its
// connection source must place nothing rather than everything.
func (e Engine) tunnelState(nodeID string) (state TunnelState, ok bool) {
	if e.Tunnels == nil {
		return TunnelDown, false
	}
	return e.Tunnels(nodeID), true
}

// tunnelStateName renders a tunnel state for an error detail, separating
// "the lookup says down" from "there is no lookup at all" — an operator
// debugging an empty placement needs to know which.
func tunnelStateName(state TunnelState, ok bool) string {
	if !ok {
		return "(no connection source wired)"
	}
	return state.String()
}
