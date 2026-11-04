package conductor

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// relayDispatch counts the two places a selection can be sent: the local
// provider of the selected lane, or a remote node.
type relayDispatch struct {
	local  int
	remote int
}

// run selects through r and dispatches the result: a selection the
// placement consult approved goes to a node, any other to the local
// provider. A refused selection dispatches nowhere.
func (d *relayDispatch) run(ctx context.Context, r *DefaultRouter, req provider.ModelRequest) (provider.Selection, error) {
	sel, flags, err := r.SelectExplain(ctx, req)
	if err != nil {
		return sel, err
	}
	if slices.ContainsFunc(flags, func(f string) bool { return strings.HasPrefix(f, "placement:eligible-nodes=") }) {
		d.remote++
		return sel, nil
	}
	d.local++
	return sel, nil
}

func TestRelayOriginRoundTrip(t *testing.T) {
	if _, ok := RelayOriginFrom(context.Background()); ok {
		t.Fatal("a bare context reported a relay origin")
	}
	want := RelayOrigin{DispatchID: "d-1", LaneID: "lane-a", Model: "model-a"}
	got, ok := RelayOriginFrom(WithRelayOrigin(context.Background(), want))
	if !ok || got != want {
		t.Fatalf("RelayOriginFrom = %+v, %t; want %+v, true", got, ok, want)
	}
}

// TestRelayOriginNeverReachesRemoteDispatcher proves a relayed call that
// asks for node placement is refused KindConflict before anything is
// dispatched, and that the same relayed call without the node demand
// runs on the local provider exactly once.
func TestRelayOriginNeverReachesRemoteDispatcher(t *testing.T) {
	fleet := fakeFleet{records: []nodes.DeviceRecord{placeableNode("node-1", "browser")}}
	r := NewRouter(oneHealthyLane(), &fakeQuota{order: []LaneID{"lane-a"}}, nil, nil).
		WithNodePlacement(nodes.Engine{Tunnels: upTunnels}, fleet)
	ctx := WithRelayOrigin(context.Background(), RelayOrigin{DispatchID: "d-1", LaneID: "lane-a", Model: "model-a"})
	d := &relayDispatch{}

	// Unrelayed, the same request is placed on the node: the double is live.
	live := &relayDispatch{}
	if _, err := live.run(context.Background(), r, nodeReq("browser")); err != nil {
		t.Fatalf("unrelayed node request: %v", err)
	}
	if live.remote != 1 || live.local != 0 {
		t.Fatalf("unrelayed: remote = %d, local = %d; want 1, 0", live.remote, live.local)
	}

	sel, err := d.run(ctx, r, nodeReq("browser"))
	if !errors.Is(err, ErrRelayNodePlacement) || err.Error() != ErrRelayNodePlacement.Error() {
		t.Fatalf("err = %v, want ErrRelayNodePlacement", err)
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindConflict {
		t.Fatalf("kind = %v (typed %t), want KindConflict", kind, ok)
	}
	if sel.LaneID != "" || d.remote != 0 || d.local != 0 {
		t.Fatalf("sel = %q, remote = %d, local = %d; want none dispatched", sel.LaneID, d.remote, d.local)
	}

	req := chatReq()
	req.Sensitivity = provider.SensitivityInternal
	sel, err = d.run(ctx, r, req)
	if err != nil {
		t.Fatalf("relayed local request: %v", err)
	}
	if sel.LaneID != "lane-a" || d.local != 1 || d.remote != 0 {
		t.Fatalf("sel = %q, local = %d, remote = %d; want lane-a, 1, 0", sel.LaneID, d.local, d.remote)
	}
}
