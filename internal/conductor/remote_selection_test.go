package conductor

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// fakeRemote records every DispatchRemote call it receives.
type fakeRemote struct {
	calls []RemoteDispatch
}

func (f *fakeRemote) DispatchRemote(_ context.Context, d RemoteDispatch) (provider.ModelResponse, error) {
	f.calls = append(f.calls, d)
	return provider.ModelResponse{Output: "remote:" + d.NodeID}, nil
}

// fakeLocal is the local-provider double: it counts calls and records the
// selection it was handed.
type fakeLocal struct {
	calls int
	sel   provider.Selection
}

func (f *fakeLocal) run(_ context.Context, sel provider.Selection, _ provider.ModelRequest) (provider.ModelResponse, error) {
	f.calls++
	f.sel = sel
	return provider.ModelResponse{Selection: sel, Output: "local"}, nil
}

// placedRouter is a router with placement wired over one eligible node.
func placedRouter(nodeID string) *DefaultRouter {
	fleet := fakeFleet{records: []nodes.DeviceRecord{placeableNode(nodeID, "browser")}}
	return NewRouter(oneHealthyLane(), &fakeQuota{order: []LaneID{"lane-a"}}, nil, nil).
		WithNodePlacement(nodes.Engine{Tunnels: upTunnels}, fleet)
}

func remoteCall() RouteCall {
	return RouteCall{Request: nodeReq("browser"), ActionID: "act-7", EntityID: "ent-9", Resume: "ckpt-3"}
}

func TestP1SelectedNodeReachesDispatch(t *testing.T) {
	remote, local := &fakeRemote{}, &fakeLocal{}
	route := NewRemoteRoute(placedRouter("node-1"), local.run).WithRemoteDispatcher(remote)

	resp, err := route.Run(context.Background(), remoteCall())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(remote.calls) != 1 {
		t.Fatalf("DispatchRemote calls = %d, want 1", len(remote.calls))
	}
	got := remote.calls[0]
	if got.NodeID != "node-1" || got.ActionID != "act-7" || got.EntityID != "ent-9" || got.Resume != "ckpt-3" {
		t.Fatalf("dispatch = %+v, want node-1/act-7/ent-9/ckpt-3", got)
	}
	if got.Request.TaskID != "t1" {
		t.Fatalf("dispatched request TaskID = %q, want t1", got.Request.TaskID)
	}
	if resp.Output != "remote:node-1" {
		t.Fatalf("Output = %q, want the remote response", resp.Output)
	}
	if local.calls != 0 {
		t.Fatalf("local provider calls = %d, want 0", local.calls)
	}

	sel, err := route.Place(context.Background(), remoteCall().Request)
	if err != nil || sel.NodeID != "node-1" {
		t.Fatalf("Place = %+v, %v; want NodeID node-1", sel, err)
	}
}

func TestRemoteSelectionNilDispatcherUnavailable(t *testing.T) {
	local := &fakeLocal{}
	route := NewRemoteRoute(placedRouter("node-1"), local.run).WithRemoteDispatcher(nil)

	_, err := route.Run(context.Background(), remoteCall())
	if !errors.Is(err, ErrRemoteUnavailable) {
		t.Fatalf("err = %v, want ErrRemoteUnavailable", err)
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindUnavailable {
		t.Fatalf("kind = %v (typed %t), want KindUnavailable", kind, ok)
	}
	for _, want := range []string{"node-1", "act-7", "ent-9", "no remote dispatcher"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %q, want it to name %q", err, want)
		}
	}
	if local.calls != 0 {
		t.Fatalf("local provider calls = %d, want 0", local.calls)
	}

	explicit := remoteCall()
	explicit.Selection = &NodeSelection{Selection: provider.Selection{LaneID: "lane-a"}, NodeID: "node-2"}
	if _, err := route.Run(context.Background(), explicit); !errors.Is(err, ErrRemoteUnavailable) {
		t.Fatalf("explicit node selection err = %v, want ErrRemoteUnavailable", err)
	}
	if local.calls != 0 {
		t.Fatalf("explicit node selection reached the local provider %d times", local.calls)
	}
}

func TestRemoteExplicitLocalUnchanged(t *testing.T) {
	remote, local := &fakeRemote{}, &fakeLocal{}
	route := NewRemoteRoute(placedRouter("node-1"), local.run).WithRemoteDispatcher(remote)

	call := RouteCall{Request: chatReq(), Selection: &NodeSelection{
		Selection: provider.Selection{LaneID: "lane-a", Provider: "prov-a", Model: "model-a"},
	}}
	resp, err := route.Run(context.Background(), call)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if local.calls != 1 || local.sel.LaneID != "lane-a" || local.sel.Model != "model-a" {
		t.Fatalf("local calls = %d sel = %+v, want one call on lane-a/model-a", local.calls, local.sel)
	}
	if resp.Output != "local" || len(remote.calls) != 0 {
		t.Fatalf("Output = %q remote calls = %d, want local and 0", resp.Output, len(remote.calls))
	}

	// With no explicit selection the router picks the same lane it did at
	// the base, and the call still runs locally.
	resp, err = route.Run(context.Background(), RouteCall{Request: chatReq()})
	if err != nil || resp.Selection.LaneID != "lane-a" || local.calls != 2 || len(remote.calls) != 0 {
		t.Fatalf("routed local: resp %+v err %v local %d remote %d", resp, err, local.calls, len(remote.calls))
	}
}
