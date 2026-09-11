package fleet

// Purpose: fleet.bench_lane's RPC-surface test suite, driven through a
//   real *rpc.Registry.Dispatch (the actual production entry point), not
//   a direct call to the handler closure. Also covers the R-16.78 §1/§2
//   lane-health wiring: persistence through a fake LaneProbeStore (no
//   real database needed to prove the call pattern) and change-only
//   publish through a REAL *events.Bus (Art.2).
// SPORT: internal.fleet.rpc.bench_lane/ADDED (P1-E12-W3-S25-T5).

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/provider"
)

// fakeLaneProbeStore is a deterministic, in-memory LaneProbeStore -- no
// real database needed to prove RegisterBenchHandlers' call pattern
// (Get-before-Upsert, Upsert always, publish only when the reading
// changed).
type fakeLaneProbeStore struct {
	rows map[string]registry.LaneProbeRecord
	gets int
}

func newFakeLaneProbeStore() *fakeLaneProbeStore {
	return &fakeLaneProbeStore{rows: map[string]registry.LaneProbeRecord{}}
}

func (s *fakeLaneProbeStore) GetLaneProbe(_ context.Context, laneName string) (*registry.LaneProbeRecord, error) {
	s.gets++
	rec, ok := s.rows[laneName]
	if !ok {
		return nil, nil
	}
	return &rec, nil
}

func (s *fakeLaneProbeStore) UpsertLaneProbe(_ context.Context, rec registry.LaneProbeRecord) error {
	s.rows[rec.LaneName] = rec
	return nil
}

func newTestBus(t *testing.T) *events.Bus {
	t.Helper()
	clock := runtime.NewFixedClock(time.Unix(1_700_000_000, 0))
	bus := events.New(storetest.NewMemStore(), clock)
	t.Cleanup(func() { _ = bus.Close() })
	return bus
}

func TestBenchLaneRPCHappyPath(t *testing.T) {
	reg := rpc.NewRegistry()
	exec := &fakeExecutor{resp: provider.ModelResponse{Usage: provider.Usage{OutputTokens: 5}}}
	RegisterBenchHandlers(reg, exec, nil, nil, nil)

	params, err := json.Marshal(benchLaneParams{LaneID: "lane-anthropic-1", Config: BenchConfig{N: 2, Concurrency: 2}})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	result, errObj := reg.Dispatch(context.Background(), &rpc.Request{Method: MethodBenchLane, Params: params})
	if errObj != nil {
		t.Fatalf("Dispatch() error = %+v, want nil", errObj)
	}
	res, ok := result.(BenchResult)
	if !ok {
		t.Fatalf("result type = %T, want BenchResult", result)
	}
	if res.ErrorRate != 0 {
		t.Fatalf("ErrorRate = %v, want 0", res.ErrorRate)
	}
}

func TestBenchLaneRPCMissingLaneIDRefused(t *testing.T) {
	reg := rpc.NewRegistry()
	RegisterBenchHandlers(reg, &fakeExecutor{}, nil, nil, nil)

	params, err := json.Marshal(map[string]any{"config": BenchConfig{N: 1}})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	_, errObj := reg.Dispatch(context.Background(), &rpc.Request{Method: MethodBenchLane, Params: params})
	if errObj == nil {
		t.Fatal("Dispatch() error = nil, want a refusal for a missing lane_id")
	}
}

func TestBenchLaneRPCUnknownFieldRefused(t *testing.T) {
	reg := rpc.NewRegistry()
	RegisterBenchHandlers(reg, &fakeExecutor{}, nil, nil, nil)

	params := json.RawMessage(`{"lane_id":"lane-x","bogus_field":true}`)
	_, errObj := reg.Dispatch(context.Background(), &rpc.Request{Method: MethodBenchLane, Params: params})
	if errObj == nil {
		t.Fatal("Dispatch() error = nil, want a refusal for an unknown params field")
	}
}

func TestBenchLaneRPCMethodRegistered(t *testing.T) {
	reg := rpc.NewRegistry()
	RegisterBenchHandlers(reg, &fakeExecutor{}, nil, nil, nil)
	if !reg.Registered(MethodBenchLane) {
		t.Fatal("fleet.bench_lane not registered after RegisterBenchHandlers")
	}
}

// TestBenchLaneRPCPersistsToStore proves a successful probe is written
// through LaneProbeStore -- the R-16.78 §1 persistence half.
func TestBenchLaneRPCPersistsToStore(t *testing.T) {
	reg := rpc.NewRegistry()
	exec := &fakeExecutor{resp: provider.ModelResponse{Usage: provider.Usage{OutputTokens: 5}}}
	store := newFakeLaneProbeStore()
	clock := runtime.NewFixedClock(time.Unix(1_700_000_000, 0))
	RegisterBenchHandlers(reg, exec, store, nil, clock)

	params, _ := json.Marshal(benchLaneParams{LaneID: "lane-anthropic-1", Config: BenchConfig{N: 1}})
	if _, errObj := reg.Dispatch(context.Background(), &rpc.Request{Method: MethodBenchLane, Params: params}); errObj != nil {
		t.Fatalf("Dispatch() error = %+v, want nil", errObj)
	}
	got, err := store.GetLaneProbe(context.Background(), "lane-anthropic-1")
	if err != nil || got == nil {
		t.Fatalf("GetLaneProbe after Dispatch = (%+v, %v), want a persisted row", got, err)
	}
}

// TestBenchLaneRPCPublishesOnlyOnChange proves R-16.78 §2's "emit on
// change only" rule end to end over a REAL *events.Bus: an unchanging
// second probe result must NOT publish a second lane-health event.
func TestBenchLaneRPCPublishesOnlyOnChange(t *testing.T) {
	reg := rpc.NewRegistry()
	exec := &fakeExecutor{resp: provider.ModelResponse{Usage: provider.Usage{OutputTokens: 5}}}
	store := newFakeLaneProbeStore()
	bus := newTestBus(t)
	clock := runtime.NewFixedClock(time.Unix(1_700_000_000, 0))
	RegisterBenchHandlers(reg, exec, store, bus, clock)
	ctx := context.Background()

	sub, err := bus.Subscribe(ctx, "fleet.sessions", "test-lane-health", 8)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer func() { _ = sub.Unsubscribe() }()

	params, _ := json.Marshal(benchLaneParams{LaneID: "lane-anthropic-1", Config: BenchConfig{N: 1}})
	dispatch := func() {
		if _, errObj := reg.Dispatch(ctx, &rpc.Request{Method: MethodBenchLane, Params: params}); errObj != nil {
			t.Fatalf("Dispatch() error = %+v, want nil", errObj)
		}
	}

	dispatch() // first probe: always a change (no prior row)
	select {
	case ev := <-sub.Events:
		if ev.Kind != "fleet.sessions.lane_health.changed" {
			t.Fatalf("event kind = %q, want fleet.sessions.lane_health.changed", ev.Kind)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the first lane-health event")
	}

	dispatch() // second probe: identical fake response, so no publish
	select {
	case ev := <-sub.Events:
		t.Fatalf("unexpected second lane-health event %+v; the reading did not change", ev)
	case <-time.After(200 * time.Millisecond):
		// correct: no second event
	}
}
