package conductor

import (
	"context"
	"errors"
	"testing"

	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// fakeFleet is a NodeFleet double: a fixed record list, or a fixed read
// failure.
type fakeFleet struct {
	records []nodes.DeviceRecord
	err     error
}

func (f fakeFleet) List() ([]nodes.DeviceRecord, error) { return f.records, f.err }

// placeableNode is an enrolled record that clears every placement filter
// except the capability match, which the caller supplies. Building it from
// the REAL field names (rather than a struct the test wishes existed) is
// what makes a later change to any filter's input break this test.
func placeableNode(id string, capabilities ...string) nodes.DeviceRecord {
	return nodes.DeviceRecord{
		NodeID:     id,
		Tier:       nodes.TierWorkerTrusted,
		Presence:   nodes.PresenceReachable,
		LastReport: nodes.CapabilityReport{Capabilities: capabilities},
	}
}

// upTunnels reports every node as connected.
func upTunnels(string) nodes.TunnelState { return nodes.TunnelUp }

// nodeReq is a request that demands node capabilities and permits a lane
// off the controller machine, so placement is consulted for real.
func nodeReq(capabilities ...string) provider.ModelRequest {
	req := chatReq()
	req.Requirements.NodeCapabilities = capabilities
	req.Sensitivity = provider.SensitivityInternal
	req.Policy.ExternalAllowed = true
	return req
}

// TestRouterConsultsNodeEligibility is the K×Q seam proof named by
// P1-E17-W4-S37-T1's own checks: the router does not merely HOLD a
// placement engine, it asks it, and the answer decides the outcome.
//
// The two halves run against one router and one fleet, differing only in
// the capability the work demands: one the node advertises, one it does
// not. A router that ignored placement would return the same selection for
// both, so the pair fails on any wiring that is present but unconsulted.
func TestRouterConsultsNodeEligibility(t *testing.T) {
	fleet := fakeFleet{records: []nodes.DeviceRecord{placeableNode("node-1", "browser", "docker")}}
	r := NewRouter(oneHealthyLane(), &fakeQuota{order: []LaneID{"lane-a"}}, nil, nil).
		WithNodePlacement(nodes.Engine{Tunnels: upTunnels}, fleet)

	sel, flags, err := r.SelectExplain(context.Background(), nodeReq("browser"))
	if err != nil {
		t.Fatalf("an advertised capability was refused: %v", err)
	}
	if sel.LaneID != "lane-a" {
		t.Fatalf("LaneID = %q, want lane-a", sel.LaneID)
	}
	if !containsPrefix(flags, "placement:eligible-nodes=1") {
		t.Fatalf("flags = %v, want the eligible-node count", flags)
	}

	_, flags, err = r.SelectExplain(context.Background(), nodeReq("gpu"))
	if err == nil {
		t.Fatal("a capability no node advertises was routed anyway")
	}
	if !containsPrefix(flags, "placement:no-eligible-node") {
		t.Fatalf("flags = %v, want the no-eligible-node flag", flags)
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindUnavailable {
		t.Fatalf("kind = %v (typed %t), want KindUnavailable", kind, ok)
	}
}

// TestRouterSkipsPlacementWithoutNodeRequirements asserts the seam is
// inert for an ordinary model call: no fleet read, no flag, no change to
// the five-filter outcome. This is what keeps the consult from taxing
// every request in the system.
func TestRouterSkipsPlacementWithoutNodeRequirements(t *testing.T) {
	fleet := &countingFleet{}
	r := NewRouter(oneHealthyLane(), &fakeQuota{order: []LaneID{"lane-a"}}, nil, nil).
		WithNodePlacement(nodes.Engine{Tunnels: upTunnels}, fleet)

	_, flags, err := r.SelectExplain(context.Background(), chatReq())
	if err != nil {
		t.Fatalf("SelectExplain: %v", err)
	}
	if fleet.calls != 0 {
		t.Fatalf("the fleet was read %d times for a request with no node requirement", fleet.calls)
	}
	for _, f := range flags {
		if len(f) >= 10 && f[:10] == "placement:" {
			t.Fatalf("flags = %v, want no placement flag", flags)
		}
	}
}

// countingFleet records how many times the router read the fleet.
type countingFleet struct{ calls int }

func (f *countingFleet) List() ([]nodes.DeviceRecord, error) {
	f.calls++
	return nil, nil
}

// TestRouterRefusesNodeWorkWithNoPlacementEngine is the anti-silent-drop
// assertion. Before this seam existed the daemon accepted
// `--require node.browser=true` and routed as though it had not been
// typed; an unwired router must refuse instead, because a caller cannot
// tell a satisfied requirement from an ignored one by looking at a
// successful response.
func TestRouterRefusesNodeWorkWithNoPlacementEngine(t *testing.T) {
	r := NewRouter(oneHealthyLane(), &fakeQuota{order: []LaneID{"lane-a"}}, nil, nil)

	_, flags, err := r.SelectExplain(context.Background(), nodeReq("browser"))
	if !errors.Is(err, ErrNodePlacementUnavailable) {
		t.Fatalf("err = %v, want ErrNodePlacementUnavailable", err)
	}
	if !containsPrefix(flags, "placement:no-engine-wired") {
		t.Fatalf("flags = %v, want the no-engine-wired flag", flags)
	}
}

// TestRouterPropagatesAFleetReadFailure asserts an unreadable fleet is a
// refusal, never an empty candidate list. "No records" and "the records
// could not be read" are different facts, and only the first one may ever
// mean "nowhere to run this".
func TestRouterPropagatesAFleetReadFailure(t *testing.T) {
	boom := errors.New("record store unreadable")
	r := NewRouter(oneHealthyLane(), &fakeQuota{order: []LaneID{"lane-a"}}, nil, nil).
		WithNodePlacement(nodes.Engine{Tunnels: upTunnels}, fakeFleet{err: boom})

	_, flags, err := r.SelectExplain(context.Background(), nodeReq("browser"))
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the store's own error", err)
	}
	if !containsPrefix(flags, "placement:fleet-unreadable") {
		t.Fatalf("flags = %v, want the fleet-unreadable flag", flags)
	}
}

// TestPlacementSensitivityFailsClosed walks every mapping from the
// router's classification onto the placement engine's, including the two
// that must resolve most restrictively: an unrecognized tier, and any
// request that forbids leaving the controller machine.
func TestPlacementSensitivityFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name     string
		tier     provider.SensitivityTier
		external bool
		want     nodes.Sensitivity
	}{
		{"internal is normal", provider.SensitivityInternal, true, nodes.SensitivityNormal},
		{"public is normal", provider.SensitivityPublic, true, nodes.SensitivityNormal},
		{"restricted stays restricted", provider.SensitivityRestricted, true, nodes.SensitivityRestricted},
		{"local-only stays local-only", provider.SensitivityLocalOnly, true, nodes.SensitivityLocalOnly},
		{"an unknown tier resolves local-only", provider.SensitivityTier(99), true, nodes.SensitivityLocalOnly},
		{"external forbidden overrides public", provider.SensitivityPublic, false, nodes.SensitivityLocalOnly},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := provider.ModelRequest{Sensitivity: tc.tier}
			req.Policy.ExternalAllowed = tc.external
			if got := placementSensitivity(req); got != tc.want {
				t.Fatalf("placementSensitivity = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestRouterRefusesNodeWorkThatMayNotLeaveTheController is the end-to-end
// reading of the rule above: pkg/provider.Policy says ExternalAllowed
// false "forces local-only placement regardless of Sensitivity", and
// local-only work is never eligible on an enrolled node however capable it
// is. The node here advertises exactly what was asked for and is still
// refused.
func TestRouterRefusesNodeWorkThatMayNotLeaveTheController(t *testing.T) {
	fleet := fakeFleet{records: []nodes.DeviceRecord{placeableNode("node-1", "browser")}}
	r := NewRouter(oneHealthyLane(), &fakeQuota{order: []LaneID{"lane-a"}}, nil, nil).
		WithNodePlacement(nodes.Engine{Tunnels: upTunnels}, fleet)

	req := nodeReq("browser")
	req.Policy.ExternalAllowed = false
	if _, _, err := r.SelectExplain(context.Background(), req); err == nil {
		t.Fatal("work that may not leave the controller was placed on a node")
	}
}

// TestWithNodePlacementIgnoresAHalfWiredSeam asserts a nil engine or a nil
// fleet leaves the router unwired rather than half-wired — a router
// holding one of the two would panic on the first node-requiring request.
func TestWithNodePlacementIgnoresAHalfWiredSeam(t *testing.T) {
	for _, tc := range []struct {
		name   string
		engine NodeEligibility
		fleet  NodeFleet
	}{
		{"nil engine", nil, fakeFleet{}},
		{"nil fleet", nodes.Engine{Tunnels: upTunnels}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := NewRouter(oneHealthyLane(), &fakeQuota{order: []LaneID{"lane-a"}}, nil, nil).
				WithNodePlacement(tc.engine, tc.fleet)
			if _, _, err := r.SelectExplain(context.Background(), nodeReq("browser")); !errors.Is(err, ErrNodePlacementUnavailable) {
				t.Fatalf("err = %v, want ErrNodePlacementUnavailable", err)
			}
		})
	}
}

// TestNodePlacementOptionWiresTheSameSeam asserts the RouterOption form
// internal/daemon's composition root uses is the method, not a second
// implementation of it.
func TestNodePlacementOptionWiresTheSameSeam(t *testing.T) {
	fleet := fakeFleet{records: []nodes.DeviceRecord{placeableNode("node-1", "browser")}}
	r := NewRouter(oneHealthyLane(), &fakeQuota{order: []LaneID{"lane-a"}}, nil, nil)
	NodePlacement(nodes.Engine{Tunnels: upTunnels}, fleet)(r)

	if _, flags, err := r.SelectExplain(context.Background(), nodeReq("browser")); err != nil {
		t.Fatalf("SelectExplain: %v (flags %v)", err, flags)
	}
}
