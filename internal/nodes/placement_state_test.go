package nodes

import (
	"strings"
	"testing"
)

// upEngine is an Engine whose every node reports an up tunnel, so a test
// can isolate a filter other than connectivity.
func upEngine() Engine { return Engine{Tunnels: func(string) TunnelState { return TunnelUp }} }

// TestDrainedNodesAreExcluded pins the operator's own decision: drain means
// "accept no new work", and it must hold even for a node that is otherwise
// perfect.
func TestDrainedNodesAreExcluded(t *testing.T) {
	rec := DeviceRecord{NodeID: "n1", Tier: TierWorkerTrusted, Presence: PresenceReachable, Drained: true}
	reason, detail, excluded := upEngine().excludedByFleetState(rec)
	if !excluded {
		t.Fatal("a drained node was offered for placement")
	}
	if reason != ReasonDrained {
		t.Fatalf("reason = %q, want %q", reason, ReasonDrained)
	}
	if !strings.Contains(detail, "drained") {
		t.Fatalf("detail = %q, want it to say the node is drained", detail)
	}

	rec.Drained = false
	if _, _, excluded := upEngine().excludedByFleetState(rec); excluded {
		t.Fatal("an undrained, reachable, connected node was excluded")
	}
}

// TestOnlyReachablePresencePlaces is the three-state liveness rule, written
// as an allow-list.
//
// The two values that matter most are the ones a "not unavailable" test
// would wrongly admit: `unknown`, which is the fail-closed default after a
// heartbeat timeout, and `remote-via-route`, which a later ticket owns and
// which this ticket's contract says is not read here. An allow-list also
// means a value added to the enum later cannot silently become placeable.
func TestOnlyReachablePresencePlaces(t *testing.T) {
	if !placeablePresence(PresenceReachable) {
		t.Fatal("reachable is not placeable")
	}
	for _, p := range []Presence{PresenceUnknown, PresenceUnavailable, PresenceRemoteViaRoute, "", "made-up"} {
		if placeablePresence(p) {
			t.Errorf("presence %q was treated as placeable", p)
		}
	}
}

// TestUnreachablePresenceIsReportedWithItsValue proves the exclusion names
// which presence was seen, so an operator can tell a timed-out node from a
// never-probed one.
func TestUnreachablePresenceIsReportedWithItsValue(t *testing.T) {
	cases := map[Presence]string{
		PresenceUnknown:        "unknown",
		PresenceUnavailable:    "unavailable",
		PresenceRemoteViaRoute: "remote-via-route",
		"":                     "(never probed)",
	}
	for presence, want := range cases {
		rec := DeviceRecord{NodeID: "n1", Tier: TierWorkerTrusted, Presence: presence}
		reason, detail, excluded := upEngine().excludedByFleetState(rec)
		if !excluded {
			t.Errorf("presence %q was placed", presence)
			continue
		}
		if reason != ReasonNotReachable {
			t.Errorf("presence %q: reason = %q, want %q", presence, reason, ReasonNotReachable)
		}
		if !strings.Contains(detail, want) {
			t.Errorf("presence %q: detail = %q, want it to contain %q", presence, detail, want)
		}
	}
}

// TestOnlyAnUpTunnelPlaces pins the connectivity rule: reconnecting is not
// connected. A node the controller is still dialing cannot receive work.
func TestOnlyAnUpTunnelPlaces(t *testing.T) {
	rec := DeviceRecord{NodeID: "n1", Tier: TierWorkerTrusted, Presence: PresenceReachable}

	for _, state := range []TunnelState{TunnelDown, TunnelReconnecting} {
		engine := Engine{Tunnels: func(string) TunnelState { return state }}
		reason, detail, excluded := engine.excludedByFleetState(rec)
		if !excluded {
			t.Errorf("tunnel state %v was treated as connected", state)
			continue
		}
		if reason != ReasonNotConnected {
			t.Errorf("tunnel %v: reason = %q, want %q", state, reason, ReasonNotConnected)
		}
		if !strings.Contains(detail, state.String()) {
			t.Errorf("tunnel %v: detail = %q, want it to name the state", state, detail)
		}
	}

	if _, _, excluded := upEngine().excludedByFleetState(rec); excluded {
		t.Error("an up tunnel was treated as not connected")
	}
}

// TestNoConnectionSourceExcludesEveryNode proves the nil-lookup case fails
// closed AND says so distinctly: an operator debugging an empty placement
// must be able to tell "the tunnel is down" from "nothing wired the tunnel
// lookup at all", which are very different bugs.
func TestNoConnectionSourceExcludesEveryNode(t *testing.T) {
	var engine Engine
	rec := DeviceRecord{NodeID: "n1", Tier: TierWorkerTrusted, Presence: PresenceReachable}

	reason, detail, excluded := engine.excludedByFleetState(rec)
	if !excluded {
		t.Fatal("an engine with no connection source offered a node for placement")
	}
	if reason != ReasonNotConnected {
		t.Fatalf("reason = %q, want %q", reason, ReasonNotConnected)
	}
	if !strings.Contains(detail, "no connection source") {
		t.Fatalf("detail = %q, want it to distinguish an unwired lookup from a down tunnel", detail)
	}
}

// TestFleetStateReportsDrainFirst pins the reporting order. A node that is
// drained AND unreachable is reported as drained, because drain is the
// deliberate operator decision and the one they would act on; reporting it
// as unreachable would send them chasing a network problem they created on
// purpose.
func TestFleetStateReportsDrainFirst(t *testing.T) {
	rec := DeviceRecord{NodeID: "n1", Tier: TierWorkerTrusted, Presence: PresenceUnknown, Drained: true}
	var engine Engine // also not connected, so all three conditions hold
	reason, _, excluded := engine.excludedByFleetState(rec)
	if !excluded {
		t.Fatal("a drained, unreachable, unconnected node was placed")
	}
	if reason != ReasonDrained {
		t.Fatalf("reason = %q, want %q reported first", reason, ReasonDrained)
	}
}

// TestPlacementLivenessThreeState is R-21.225 asserted through the real
// engine rather than through placeablePresence alone: liveness is the
// three-state {reachable, unavailable, unknown} result, `unknown` is the
// fail-closed default on heartbeat timeout, and ANYTHING other than
// reachable is not placeable. There is no binary heartbeat-dead test to
// get wrong.
//
// remote-via-route and the never-probed empty value ride along because
// they are values a record can really carry, and a filter written as
// "not unavailable" would place all three of the non-reachable ones.
func TestPlacementLivenessThreeState(t *testing.T) {
	req := Requirement{Sensitivity: SensitivityNormal}
	for _, presence := range []Presence{PresenceUnknown, PresenceUnavailable, PresenceRemoteViaRoute, ""} {
		rec := DeviceRecord{NodeID: "n1", Tier: TierWorkerTrusted, Presence: presence}
		eligible, err := upEngine().Eligible(req, []Candidate{{Record: rec}})
		if err == nil {
			t.Errorf("presence %q placed %d node(s); only reachable may place", presence, len(eligible))
		}
	}
	reachable := DeviceRecord{NodeID: "n1", Tier: TierWorkerTrusted, Presence: PresenceReachable}
	eligible, err := upEngine().Eligible(req, []Candidate{{Record: reachable}})
	if err != nil {
		t.Fatalf("a reachable node was refused: %v", err)
	}
	if len(eligible) != 1 || eligible[0].NodeID != "n1" {
		t.Fatalf("eligible = %+v, want exactly n1", eligible)
	}
}
