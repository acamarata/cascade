package daemon

// Purpose: covers RegisterContextSyncHandler and ComputeContextSync
//   (context_sync.go), E/S-09.T4's daemon-side registration, mirroring
//   context_scope_test.go's own real-registry-and-Dispatch pattern rather
//   than calling ComputeContextSync directly.
// Constraints: Art.7.1 -- no real network listener; every case drives the
//   real rpc.Registry.Dispatch entry point.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
)

// TestRegisterContextSyncHandler_RegistersAndDispatches proves the
// registration line matters: it drives context.sync through the REAL
// registry.Dispatch entry point against an empty temp directory, so the
// honest result is zero drift entries (no tier files exist to resolve)
// rather than a fabricated fixture standing in for the real pipeline.
//
// HOME is pinned to a second, empty temp directory (Art.7.1): production
// resolves the GCI tier through the real os.UserHomeDir
// (ComputeContextSync's own doc comment), so a test that left the
// developer's actual $HOME in place would read whatever real
// ~/.cascade/CASCADE.md happens to exist on the machine running it —
// exactly the kind of environment-dependent result this test must not
// have.
func TestRegisterContextSyncHandler_RegistersAndDispatches(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	paths := fakePathsFor(t, "")
	clock := runtime.NewSystemClock()
	registry := rpc.NewRegistry()

	if err := RegisterContextSyncHandler(registry, paths, clock); err != nil {
		t.Fatalf("RegisterContextSyncHandler: %v", err)
	}
	if !registry.Registered(ContextSyncMethod) {
		t.Fatal("RegisterContextSyncHandler did not register ContextSyncMethod")
	}

	dir := t.TempDir()
	raw, err := json.Marshal(ContextSyncParams{Cwd: dir, CheckOnly: true})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	result, errObj := registry.Dispatch(context.Background(), &rpc.Request{
		JSONRPC: "2.0", Method: ContextSyncMethod, Params: raw, ID: json.RawMessage(`1`),
	})
	if errObj != nil {
		t.Fatalf("Dispatch(%s) returned error: %+v", ContextSyncMethod, errObj)
	}
	got, ok := result.(ContextSyncResult)
	if !ok {
		t.Fatalf("Dispatch result type = %T, want ContextSyncResult", result)
	}
	if len(got.Drift) != 0 {
		t.Errorf("Drift = %+v, want empty (no tiers resolve under an empty temp dir)", got.Drift)
	}
}

// TestRegisterContextSyncHandler_MalformedParams requires malformed wire
// params to fail closed with a typed error, not a zero-value guess.
func TestRegisterContextSyncHandler_MalformedParams(t *testing.T) {
	registry := rpc.NewRegistry()
	if err := RegisterContextSyncHandler(registry, fakePathsFor(t, ""), runtime.NewSystemClock()); err != nil {
		t.Fatalf("RegisterContextSyncHandler: %v", err)
	}
	_, errObj := registry.Dispatch(context.Background(), &rpc.Request{
		JSONRPC: "2.0", Method: ContextSyncMethod, Params: json.RawMessage(`{"cwd": 5}`), ID: json.RawMessage(`1`),
	})
	if errObj == nil {
		t.Fatal("Dispatch with malformed params returned no error, want invalid-input")
	}
}

// TestRegisterContextSyncHandler_WiringProof is this file's own mutation
// lever: dispatching ContextSyncMethod against a registry
// RegisterContextSyncHandler never touched must fail with "method not
// found", proving the tests above exercise the real registration line
// rather than a vacuously-passing fixture.
func TestRegisterContextSyncHandler_WiringProof(t *testing.T) {
	registry := rpc.NewRegistry()
	_, errObj := registry.Dispatch(context.Background(), &rpc.Request{
		JSONRPC: "2.0", Method: ContextSyncMethod, Params: nil, ID: json.RawMessage(`1`),
	})
	if errObj == nil {
		t.Fatal("Dispatch on an unregistered method returned no error")
	}
}
