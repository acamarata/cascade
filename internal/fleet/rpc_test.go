package fleet

// Purpose: fleet.bench_lane's RPC-surface test suite, driven through a
//   real *rpc.Registry.Dispatch (the actual production entry point), not
//   a direct call to the handler closure.
// SPORT: internal.fleet.rpc.bench_lane/ADDED (P1-E12-W3-S25-T5).

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/provider"
)

func TestBenchLaneRPCHappyPath(t *testing.T) {
	reg := rpc.NewRegistry()
	exec := &fakeExecutor{resp: provider.ModelResponse{Usage: provider.Usage{OutputTokens: 5}}}
	RegisterBenchHandlers(reg, exec)

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
	RegisterBenchHandlers(reg, &fakeExecutor{})

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
	RegisterBenchHandlers(reg, &fakeExecutor{})

	params := json.RawMessage(`{"lane_id":"lane-x","bogus_field":true}`)
	_, errObj := reg.Dispatch(context.Background(), &rpc.Request{Method: MethodBenchLane, Params: params})
	if errObj == nil {
		t.Fatal("Dispatch() error = nil, want a refusal for an unknown params field")
	}
}

func TestBenchLaneRPCMethodRegistered(t *testing.T) {
	reg := rpc.NewRegistry()
	RegisterBenchHandlers(reg, &fakeExecutor{})
	if !reg.Registered(MethodBenchLane) {
		t.Fatal("fleet.bench_lane not registered after RegisterBenchHandlers")
	}
}
