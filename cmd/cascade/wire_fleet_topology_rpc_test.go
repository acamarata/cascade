//go:build !windows

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/fleet/topology"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

func composeTopology(t *testing.T) (*daemonWiring, topology.Lane) {
	t.Helper()
	home := t.TempDir()
	for _, name := range []string{"HOME", "USERPROFILE", "CASCADE_HOME"} {
		t.Setenv(name, home)
	}
	seen := observeWiring(t)
	deps := daemonDeps{
		Paths: fakeDaemonPaths{root: home}, Getenv: func(string) string { return "" },
		Environ:    func() []string { return nil },
		Clock:      runtime.NewFixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
		Executable: nonexistentExecutable(t),
	}
	_, cleanups, err := composeDaemon(context.Background(), deps, nil)
	t.Cleanup(func() { runCleanupsLIFO(cleanups) })
	if err != nil {
		t.Fatal(err)
	}
	w := <-seen
	seedCapProvider(t, w.Deps.Fleet.CapacityParts().Registry, registry.ProviderRecord{
		Name: "provider-x", Driver: registry.DriverOpenAICompat, BaseURL: "https://api.invalid/v1",
		Auth: registry.AuthKey, Tier: registry.TierMid,
	}, registry.LaneRecord{LaneName: "lane-a"})
	s := w.Deps.Fleet.Topology()
	if _, err := topology.Reconcile(w.Ctx, s, w.Deps.Fleet.CapacityParts().Registry, w.Clock); err != nil {
		t.Fatal(err)
	}
	lanes, err := s.ListLanes(w.Ctx, false)
	if err != nil || len(lanes) != 1 {
		t.Fatalf("seeded lanes = %+v, %v; want one", lanes, err)
	}
	return w, lanes[0]
}

func topologyRequests(lane topology.Lane) []rpc.Request {
	return []rpc.Request{
		{Method: "fleet.lanes.list"},
		{Method: "fleet.lanes.show", Params: json.RawMessage(`{"id":"` + string(lane.ID) + `"}`)},
		{Method: "fleet.lanes.explain", Params: json.RawMessage(`{"id":"` + string(lane.ID) + `"}`)},
		{Method: "fleet.domains.list"},
		{Method: "fleet.domains.show", Params: json.RawMessage(`{"id":"` + string(lane.QuotaDomainRef) + `"}`)},
	}
}

func TestFleetTopologyRPCRegistered(t *testing.T) {
	w, lane := composeTopology(t)
	owner := principal.WithCaller(w.Ctx, principal.Caller{OwnerCLI: true})
	p := principal.Principal{ID: "session-a", Kind: principal.KindSession, Scope: "project:test"}
	caller := principal.WithCaller(w.Ctx, principal.Caller{Principal: &p})
	for _, req := range topologyRequests(lane) {
		t.Run(req.Method, func(t *testing.T) {
			result, eo := w.Registry.Dispatch(owner, &req)
			if eo != nil {
				t.Fatalf("owner dispatch: %+v", eo)
			}
			raw, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			var envelope struct {
				Version string          `json:"version"`
				Data    json.RawMessage `json:"data"`
			}
			if err := json.Unmarshal(raw, &envelope); err != nil || envelope.Version != "1" {
				t.Fatalf("envelope = %s, %v", raw, err)
			}
			want := string(lane.ID)
			if strings.HasPrefix(req.Method, "fleet.domains.") {
				want = string(lane.QuotaDomainRef)
			}
			if !strings.Contains(string(envelope.Data), `"id":"`+want+`"`) {
				t.Fatalf("seeded row missing: %s", raw)
			}
			_, eo = w.Registry.Dispatch(caller, &req)
			if eo == nil || eo.Message != "caller not permitted" || fmt.Sprint(eo.Data) != fmt.Sprint(map[string]string{"kind": cascade.KindPermissionDenied.String()}) {
				t.Fatalf("principal dispatch = %+v", eo)
			}
		})
	}
}

func TestFleetTopologyAccessClasses(t *testing.T) {
	w, lane := composeTopology(t)
	// A principal is admitted by OwnerView, so its refusal distinguishes
	// OwnerOnly from both OwnerView and Open on the real dispatcher.
	p := principal.Principal{ID: "session-a", Kind: principal.KindSession, Scope: "project:test"}
	for _, req := range topologyRequests(lane) {
		for _, ctx := range []context.Context{context.Background(), principal.WithCaller(w.Ctx, principal.Caller{Principal: &p})} {
			_, eo := w.Registry.Dispatch(ctx, &req)
			if eo == nil || eo.Message != "caller not permitted" {
				t.Fatalf("%s admitted non-owner: %+v", req.Method, eo)
			}
		}
		if _, eo := w.Registry.Dispatch(principal.WithCaller(w.Ctx, principal.Caller{}), &req); eo != nil {
			t.Fatalf("%s refused owner: %+v", req.Method, eo)
		}
	}
	if _, ok := w.Events.Topics[rpc.FleetTopologyTopic].(*rpc.SSEHandler); !ok {
		t.Fatal("fleet.topology handler missing")
	}
	// Topic access has no exported inspection seam. Check its declaration
	// structurally without opening a socket in this untagged unit test.
	f, err := parser.ParseFile(token.NewFileSet(), "../../internal/rpc/fleet_topology_sse.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	marked := false
	ast.Inspect(f, func(n ast.Node) bool {
		c, ok := n.(*ast.CallExpr)
		if !ok || len(c.Args) != 2 {
			return true
		}
		fn, _ := c.Fun.(*ast.Ident)
		topic, _ := c.Args[0].(*ast.Ident)
		access, _ := c.Args[1].(*ast.Ident)
		if fn != nil && fn.Name == "DeclareTopicAccess" && topic != nil && topic.Name == "FleetTopologyTopic" {
			marked = access != nil && access.Name == "AccessOwnerOnly"
		}
		return true
	})
	if !marked {
		t.Fatal("fleet.topology must declare AccessOwnerOnly")
	}
}

func TestDaemonPublishesLaneHealthChanged(t *testing.T) {
	w, lane := composeTopology(t)
	s := w.Deps.Fleet.Topology()
	before, err := w.Bus.Replay(w.Ctx, rpc.FleetTopologyTopic, 0)
	if err != nil {
		t.Fatal(err)
	}
	var offset uint64
	if len(before) > 0 {
		offset = before[len(before)-1].Seq
	}
	from := lane.Health
	lane.Health = topology.LaneHealthExhausted
	if from == lane.Health {
		lane.Health = topology.LaneHealthAvailable
	}
	if err := s.UpsertLanes(w.Ctx, []topology.Lane{lane}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertLanes(w.Ctx, []topology.Lane{lane}); err != nil {
		t.Fatal(err)
	}
	rows, err := w.Bus.Replay(w.Ctx, rpc.FleetTopologyTopic, offset)
	if err != nil || len(rows) != 1 {
		t.Fatalf("committed transition events = %+v, %v; want one", rows, err)
	}
	var payload rpc.LaneHealthChangedPayload
	if err := json.Unmarshal(rows[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if rows[0].Kind != rpc.EventLaneHealthChanged || !rows[0].Timestamp.Equal(w.Clock.Now()) || payload.SchemaVersion != rpc.FleetTopologySchemaVersion || payload.LaneID != string(lane.ID) || payload.From != string(from) || payload.To != string(lane.Health) {
		t.Fatalf("health event = %+v, %+v", rows[0], payload)
	}
	stored, err := s.GetLane(w.Ctx, lane.ID)
	if err != nil || stored.Health != lane.Health {
		t.Fatalf("committed lane = %+v, %v", stored, err)
	}
}

func TestFleetTopologyObserverRefusesRebindingAndRollback(t *testing.T) {
	w, lane := composeTopology(t)
	err := wireFleetTopologyTopic(w)
	again := wireFleetTopologyTopic(w)
	if err == nil || err != again || err.Error() != "conflict: topology: lane health observer already set" {
		t.Fatalf("observer rebinding = %v / %v; want original conflict sentinel", err, again)
	}
	before, err := w.Bus.Replay(w.Ctx, rpc.FleetTopologyTopic, 0)
	if err != nil {
		t.Fatal(err)
	}
	lane.Health = topology.LaneHealthExhausted
	if err := w.Deps.Fleet.Topology().UpsertLanes(w.Ctx, []topology.Lane{lane, {ID: "invalid-lane"}}); err == nil {
		t.Fatal("invalid batch committed")
	}
	after, err := w.Bus.Replay(w.Ctx, rpc.FleetTopologyTopic, 0)
	if err != nil || len(after) != len(before) {
		t.Fatalf("rollback changed event count %d -> %d, %v", len(before), len(after), err)
	}
}
