//go:build !windows

// Purpose: the composition-root proof for the memory forget pipeline. It
//
//	drives memory.forget through the SAME registry registerMemoryHandler
//	builds, so the test fails if the pipeline is ever dropped from the
//	wiring — which is the only way the difference between a bare
//	tombstone and a retirement can be caught. A handler tested with a
//	pipeline supplied by the test proves the pipeline works, never that
//	the daemon uses it.
//
// SPORT: cmd/cascade/daemon (CHANGED — P1-E07-W2-S14-T4 forget wiring
//
//	verification).
package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/memory"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/testkit"
)

// forgetWiringEpoch is the instant the frozen clock starts at.
var forgetWiringEpoch = time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)

// TestRegisterMemoryHandler_ForgetRunsThePipeline dispatches
// memory.remember and then memory.forget against the registry the
// composition root builds, and asserts on the one artefact only the
// pipeline produces: the account file under memory/forgotten.
//
// Dropping memoryForgetOption from registerMemoryHandler makes this test
// fail on that file, which is exactly the regression it exists for.
func TestRegisterMemoryHandler_ForgetRunsThePipeline(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	paths := fakeDaemonPaths{root: dir}
	clock := testkit.NewFrozenClock(forgetWiringEpoch)

	registry := rpc.NewRegistry()
	registerMemoryHandler(registry, paths, clock, nil, nil)

	id := dispatchRemember(ctx, t, registry)
	res := dispatchForget(ctx, t, registry, id)

	if !res.Forgotten || res.ID != id {
		t.Fatalf("forget result = %+v, want the record retired", res)
	}
	account := filepath.Join(memoryStoreDir(paths), "forgotten", "project", "wired.forget.json")
	if _, err := os.Stat(account); err != nil {
		t.Fatalf("no forget account at %s: the daemon registered a handler with no "+
			"pipeline, so memory.forget tombstones and stops: %v", filepath.Base(account), err)
	}
	if len(res.Traces) == 0 {
		t.Fatal("the wired verb reported no traces, so a caller cannot see what survived")
	}
	for _, place := range []string{"record file", "tombstone", "record bytes on disk"} {
		if !mentions(res.Traces, place) {
			t.Errorf("the wired verb did not report %q", place)
		}
	}
}

// dispatchRemember writes one record through the registry and returns its
// canonical address.
func dispatchRemember(ctx context.Context, t *testing.T, registry *rpc.Registry) string {
	t.Helper()
	var out memory.RememberResult
	dispatch(ctx, t, registry, memory.MethodRemember, memory.RememberParams{
		Content: "the wired body\n", Type: "project", Name: "wired",
	}, &out)
	return out.ID
}

// dispatchForget retires one record through the registry.
func dispatchForget(
	ctx context.Context, t *testing.T, registry *rpc.Registry, id string,
) memory.ForgetResult {
	t.Helper()
	var out memory.ForgetResult
	dispatch(ctx, t, registry, memory.MethodForget, memory.ForgetParams{
		ID: id, Reason: "asked to",
	}, &out)
	return out
}

// dispatch runs one method through the registry and decodes its result the
// way the wire would, so nothing here bypasses the registration under test.
func dispatch(ctx context.Context, t *testing.T, registry *rpc.Registry, method string, params, out any) {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("encoding %s params: %v", method, err)
	}
	res, rpcErr := registry.Dispatch(ctx, &rpc.Request{
		JSONRPC: "2.0", Method: method, Params: raw, ID: json.RawMessage(`1`),
	})
	if rpcErr != nil {
		t.Fatalf("%s: %+v", method, rpcErr)
	}
	body, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("encoding %s result: %v", method, err)
	}
	if err := json.Unmarshal(body, out); err != nil {
		t.Fatalf("decoding %s result: %v", method, err)
	}
}

// mentions reports whether the traces name a place.
func mentions(traces []memory.ForgetTrace, place string) bool {
	for _, tr := range traces {
		if tr.Place == place {
			return true
		}
	}
	return false
}
