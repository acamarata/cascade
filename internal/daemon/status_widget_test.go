package daemon

// Purpose (this file): real-entry-point tests for status.widget (task 7)
// and status.widget_changed (task 8's SSE half): TestStatusWidgetRPC
// dispatches a real status.widget request through a real
// *rpc.Registry.Dispatch and captures the fixture (see testdata/README.md
// for the same CONTRACT DEVIATION internal/fleet/capacity's own
// TestFleetCapacityRPC already recorded — Registry.Dispatch, not a
// literal unix socket, since this test runs in the default,
// non-`integration` build lane). TestStatusWidgetChangedSSE proves both
// real triggers (a Compositor tick, an attention-queue Push) actually
// reach a real *events.Bus subscriber bound to the "daemon" namespace —
// the exact reachability S40-T4's own trap taught this phase to verify,
// never assume.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/supervision"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
)

// fakeWidgetNodeSource is a controllable capacity.NodeSource — the real
// nodes.NewRecordStore RegisterStatusWidgetHandler wires needs an actual
// enrolled device (an ed25519 identity) to report a non-empty List, more
// machinery than this test's Compositor-tick trigger needs; swapping
// deps.nodeSrc (package-private, same-package test) for this fake is the
// direct way to make UpdateNodes observe a real before/after change.
type fakeWidgetNodeSource struct{ devices []nodes.DeviceRecord }

func (f *fakeWidgetNodeSource) List() ([]nodes.DeviceRecord, error) { return f.devices, nil }

func setupStatusWidget(t *testing.T, bus *events.Bus) (*rpc.Registry, *StatusWidgetDeps) {
	t.Helper()
	root := t.TempDir()
	paths := fakePaths{root: root}
	if err := os.MkdirAll(paths.DataDir(), 0o700); err != nil {
		t.Fatalf("MkdirAll(DataDir): %v", err)
	}
	clock := runtime.NewFixedClock(time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC))
	store := storetest.NewMemStore()
	registry := rpc.NewRegistry()
	deps, err := RegisterStatusWidgetHandler(registry, store, clock, bus, paths, func() bool { return false })
	if err != nil {
		t.Fatalf("RegisterStatusWidgetHandler: %v", err)
	}
	if deps == nil {
		t.Fatal("RegisterStatusWidgetHandler returned a nil deps for a real store")
	}
	// Registered after t.TempDir()'s own cleanup (line above), so t.Cleanup's
	// LIFO order runs this FIRST: the jobs-domain cascade.db connection is
	// closed before TempDir tries to remove the directory it lives in.
	// Without this, RemoveAll fails on Windows (open-file delete refusal)
	// though it passes silently on POSIX, which unlinks an open file.
	t.Cleanup(func() { _ = deps.jobsCloser() })
	return registry, deps
}

func TestStatusWidgetRPC(t *testing.T) {
	registry, _ := setupStatusWidget(t, nil)
	if !registry.Registered(MethodStatusWidget) {
		t.Fatal("status.widget never reached the registry")
	}

	req := &rpc.Request{JSONRPC: "2.0", Method: MethodStatusWidget, ID: json.RawMessage(`1`)}
	result, errObj := registry.Dispatch(context.Background(), req)
	if errObj != nil {
		t.Fatalf("Dispatch(%s) error = %+v", MethodStatusWidget, errObj)
	}

	respBytes, err := json.Marshal(struct {
		Request json.RawMessage `json:"request"`
		Result  any             `json:"result"`
	}{Request: mustMarshalWidget(t, req), Result: result})
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	writeWidgetFixture(t, respBytes)
}

func TestStatusWidgetRPC_DaemonNotReady(t *testing.T) {
	registry := rpc.NewRegistry()
	registry.Register(MethodStatusWidget, statusWidgetHandler(nil))
	req := &rpc.Request{JSONRPC: "2.0", Method: MethodStatusWidget}
	_, errObj := registry.Dispatch(context.Background(), req)
	if errObj == nil {
		t.Fatal("expected a typed error for a nil deps, got none")
	}
}

// TestStatusWidgetChangedSSE proves both real triggers reach a real
// subscriber under the "daemon" namespace: a Compositor tick (via
// UpdateProviders) and an attention-queue Push through deps' OWN Store
// instance (status_widget_sse.go's ATTENTION-PUSH TRIGGER note — a Push
// issued through a DIFFERENT Store instance over the same data would not
// fire this, a disclosed, narrower limitation that file's own doc comment
// records).
func TestStatusWidgetChangedSSE(t *testing.T) {
	clock := runtime.NewFixedClock(time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC))
	store := storetest.NewMemStore()
	bus := events.New(store, clock)

	root := t.TempDir()
	paths := fakePaths{root: root}
	if err := os.MkdirAll(paths.DataDir(), 0o700); err != nil {
		t.Fatalf("MkdirAll(DataDir): %v", err)
	}
	registry := rpc.NewRegistry()
	deps, err := RegisterStatusWidgetHandler(registry, store, clock, bus, paths, func() bool { return false })
	if err != nil {
		t.Fatalf("RegisterStatusWidgetHandler: %v", err)
	}
	// See setupStatusWidget's identical cleanup for why this must be
	// registered here, after t.TempDir() above: LIFO closes the handle
	// before TempDir's RemoveAll runs.
	t.Cleanup(func() { _ = deps.jobsCloser() })

	ctx := context.Background()
	sub, err := bus.Subscribe(ctx, statusWidgetNamespace, "test-cursor", 8)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer func() { _ = sub.Unsubscribe() }()

	// Trigger 1: a Compositor tick — swap in a fake node source reporting
	// a real device, so the diff from the empty starting snapshot is
	// material (see fakeWidgetNodeSource's own doc comment).
	deps.nodeSrc = &fakeWidgetNodeSource{devices: []nodes.DeviceRecord{
		{NodeID: "node1", Presence: nodes.PresenceReachable, Tier: nodes.TierWorkerTrusted},
	}}
	if err := deps.comp.UpdateNodes(ctx, deps.nodeSrc); err != nil {
		t.Fatalf("UpdateNodes: %v", err)
	}

	firstSeq := awaitStatusWidgetEvent(t, sub.Events, "one Compositor tick")
	if firstSeq == 0 {
		t.Error("first event Seq = 0, want a positive monotonic sequence number")
	}

	// Trigger 2: an attention-queue push, through deps' own Store.
	if _, err := deps.attention.Push(ctx, supervision.AttentionItem{
		Kind:      supervision.KindStall,
		SourceRef: "session-1",
		ScopeRef:  defaultWidgetScope(),
	}); err != nil {
		t.Fatalf("Push: %v", err)
	}

	secondSeq := awaitStatusWidgetEvent(t, sub.Events, "one attention-queue push")
	if secondSeq <= firstSeq {
		t.Errorf("second event Seq = %d, want > %d (monotonic)", secondSeq, firstSeq)
	}
}

// awaitStatusWidgetEvent waits up to two seconds for one
// status.widget_changed event on events, failing the test (naming
// withinDesc in the message) if none arrives in time, and returns its
// decoded Seq.
func awaitStatusWidgetEvent(t *testing.T, ch <-chan events.Event, withinDesc string) uint64 {
	t.Helper()
	select {
	case ev := <-ch:
		if ev.Kind != StatusWidgetChangedKind {
			t.Fatalf("event kind = %q, want %q", ev.Kind, StatusWidgetChangedKind)
		}
		var payload struct {
			Seq uint64 `json:"seq"`
		}
		if err := json.Unmarshal(ev.Payload, &payload); err != nil {
			t.Fatalf("unmarshal payload: %v", err)
		}
		return payload.Seq
	case <-time.After(2 * time.Second):
		t.Fatalf("no status.widget_changed event within %s", withinDesc)
		return 0
	}
}

func mustMarshalWidget(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// writeWidgetFixture writes b to testdata/fixture_status_widget_rpc.json.
// Run this test to regenerate the fixture; see testdata/README.md.
func writeWidgetFixture(t *testing.T, b []byte) {
	t.Helper()
	path := filepath.Join("testdata", "fixture_status_widget_rpc.json")
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
