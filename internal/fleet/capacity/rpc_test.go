package capacity

// Purpose (this file): real-entry-point tests for the fleet.capacity
// method and the fleet.capacity_changed SSE mirror, driven through a
// real *rpc.Registry.Dispatch (Art.2's real counterpart - matching
// internal/conversation/adapter_test.go's identical precedent), plus the
// daemon-not-ready error path. See testdata/README.md for
// fixture_snapshot_rpc.json's provenance.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/rpc"
)

type fakeBus struct {
	published []json.RawMessage
}

func (f *fakeBus) Publish(_ context.Context, _ string, _ events.EventKind, _ string, payload []byte) (events.Event, error) {
	f.published = append(f.published, json.RawMessage(payload))
	return events.Event{Seq: uint64(len(f.published))}, nil
}

func dispatch(t *testing.T, reg *rpc.Registry, method string) (any, *rpc.ErrorObject) {
	t.Helper()
	return reg.Dispatch(context.Background(), &rpc.Request{JSONRPC: "2.0", Method: method})
}

func TestFleetCapacityRPC(t *testing.T) {
	clk := &fakeClock{t: time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)}
	comp := NewCompositor(clk, time.Hour, "")
	if err := comp.UpdateProviders(context.Background(), &fakeProviderSource{}); err != nil {
		t.Fatalf("UpdateProviders: %v", err)
	}

	reg := rpc.NewRegistry()
	RegisterHandlers(reg, comp)

	req := &rpc.Request{JSONRPC: "2.0", Method: MethodFleetCapacity, ID: json.RawMessage(`1`)}
	result, errObj := reg.Dispatch(context.Background(), req)
	if errObj != nil {
		t.Fatalf("Dispatch(%s) error = %+v", MethodFleetCapacity, errObj)
	}
	snap, ok := result.(FleetSnapshot)
	if !ok {
		t.Fatalf("result type = %T, want FleetSnapshot", result)
	}

	respBytes, err := json.Marshal(struct {
		Request json.RawMessage `json:"request"`
		Result  FleetSnapshot   `json:"result"`
	}{Request: mustMarshal(t, req), Result: snap})
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	writeFixture(t, respBytes)
}

func TestFleetCapacityRPC_DaemonNotReady(t *testing.T) {
	reg := rpc.NewRegistry()
	RegisterHandlers(reg, nil)

	_, errObj := dispatch(t, reg, MethodFleetCapacity)
	if errObj == nil {
		t.Fatal("expected a typed error for a nil compositor, got none")
	}
}

func TestFleetCapacitySSEFires(t *testing.T) {
	clk := &fakeClock{t: time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)}
	comp := NewCompositor(clk, time.Hour, "")
	bus := &fakeBus{}
	WireSSE(comp, bus)

	src := &fakeProviderSource{
		providers: []registry.ProviderRecord{{Name: "acme"}},
		lanes:     []registry.LaneRecord{{ProviderName: "acme", Capacity: BucketInteractiveUsage, State: registry.LaneStateAvailable}},
	}
	if err := comp.UpdateProviders(context.Background(), src); err != nil {
		t.Fatalf("UpdateProviders: %v", err)
	}
	if len(bus.published) != 1 {
		t.Fatalf("published count after first update = %d, want 1 (fires within the same tick as the change)", len(bus.published))
	}

	var payload changedPayload
	if err := json.Unmarshal(bus.published[0], &payload); err != nil {
		t.Fatalf("unmarshal SSE payload: %v", err)
	}
	if payload.Seq != 1 {
		t.Errorf("first SSE seq = %d, want 1", payload.Seq)
	}
	if payload.Delta == nil || len(payload.Delta.ChangedProviders) != 1 {
		t.Errorf("delta = %+v, want one changed provider", payload.Delta)
	}

	// A second update with no actual change must not fire again.
	if err := comp.UpdateProviders(context.Background(), src); err != nil {
		t.Fatalf("UpdateProviders (no-op): %v", err)
	}
	if len(bus.published) != 1 {
		t.Errorf("published count after a no-op update = %d, want still 1", len(bus.published))
	}

	// A real change increments Seq monotonically.
	src.lanes[0].State = registry.LaneStateExhausted
	if err := comp.UpdateProviders(context.Background(), src); err != nil {
		t.Fatalf("UpdateProviders (changed): %v", err)
	}
	if len(bus.published) != 2 {
		t.Fatalf("published count after a real change = %d, want 2", len(bus.published))
	}
	var second changedPayload
	if err := json.Unmarshal(bus.published[1], &second); err != nil {
		t.Fatalf("unmarshal second SSE payload: %v", err)
	}
	if second.Seq != 2 {
		t.Errorf("second SSE seq = %d, want 2 (monotonic)", second.Seq)
	}
}

func TestWireSSENilCompositorIsNoop(_ *testing.T) {
	// Must not panic: WireSSE(nil, ...) is a documented no-op.
	WireSSE(nil, &fakeBus{})
}

func mustMarshal(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// writeFixture writes b to testdata/fixture_snapshot_rpc.json. Run this
// test to regenerate the fixture; see testdata/README.md for provenance.
func writeFixture(t *testing.T, b []byte) {
	t.Helper()
	path := filepath.Join("testdata", "fixture_snapshot_rpc.json")
	var pretty map[string]any
	if err := json.Unmarshal(b, &pretty); err != nil {
		t.Fatalf("unmarshal for pretty-print: %v", err)
	}
	out, err := json.MarshalIndent(pretty, "", "  ")
	if err != nil {
		t.Fatalf("marshal indent: %v", err)
	}
	if err := os.WriteFile(path, append(out, '\n'), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
}
