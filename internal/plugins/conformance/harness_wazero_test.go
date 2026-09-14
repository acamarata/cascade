// Package conformance (harness_wazero_test.go): Purpose: wasmHarness drives
// the ABI v1 conformance fixtures through the REAL wazero runtime
// (internal/plugins/wasm) against the REAL compiled guest artifact
// testdata/guests/fixture.wasm (Art.2 -- provenance in
// testdata/guests/README.md; not a self-authored Go stub). Every call
// crosses an actual WebAssembly linear-memory boundary: the request is
// marshaled, written into the guest module's own memory, the guest's
// exported wrapper is called, and the response is read back out of guest
// memory -- exactly what a real WASM plugin's host-fn call does.
//
// SPORT: internal.plugins.conformance/ADDED (P1-E15-W4-S32-T2),
// plugin/wasm-runtime conformance-coverage=yes.
package conformance

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/plugins/wasm"
)

// wasmHarnessNetScopes is the fixed net-scope grant every wasmHarness
// call runs under: the happy-path host_http fixture's URL is inside it,
// the error-path fixture's URL is deliberately outside it.
var wasmHarnessNetScopes = []string{"allowed.example.com"}

// wasmHarness is not usable at its zero value; use newWasmHarness.
type wasmHarness struct {
	rt   *wasm.Runtime
	lm   *wasm.LoadedModule
	deps wasm.Deps
}

// newWasmHarness builds a real wazero Runtime, loads the real
// testdata/guests/fixture.wasm guest artifact through it, and wires its
// Deps to a fresh refSink -- the SAME delegate shape builtinHarness calls
// directly, so a fixture's happy-path response is byte-identical between
// the two once wasm's own read/dispatch/write plumbing is proven correct.
func newWasmHarness(ctx context.Context, t *testing.T) *wasmHarness {
	t.Helper()
	rt, err := wasm.NewRuntime(ctx, 64)
	if err != nil {
		t.Fatalf("wasm.NewRuntime: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close(ctx) })

	guestBytes, err := os.ReadFile(filepath.Join("testdata", "guests", "fixture.wasm"))
	if err != nil {
		t.Fatalf("read fixture.wasm guest artifact: %v", err)
	}
	lm, err := rt.Loader().Load(ctx, "conformance-fixture", guestBytes)
	if err != nil {
		t.Fatalf("Load fixture.wasm: %v", err)
	}

	sink := newRefSink()
	return &wasmHarness{
		rt: rt, lm: lm,
		deps: wasm.Deps{
			Logger: sink, Events: sink, Stream: sink, Tools: sink,
			Storage: sink, Secrets: sink, Net: sink,
		},
	}
}

func (h *wasmHarness) Name() string         { return "wasm" }
func (h *wasmHarness) CapabilityOnly() bool { return false }

// Call implements ABIHarness by dispatching f's request through the real
// wazero call boundary into fixture.wasm's exported wrapper for f.Method.
func (h *wasmHarness) Call(ctx context.Context, t *testing.T, f HostFnFixture) ABIObservation {
	t.Helper()
	export, ok := wasmGuestExport[f.Method]
	if !ok {
		t.Fatalf("wasmHarness: no guest export mapped for method %q", f.Method)
	}
	req, out := wasmRequestResponsePair(f)
	if err := json.Unmarshal(f.WasmRequest, req); err != nil {
		t.Fatalf("wasmHarness: decode fixture request: %v", err)
	}
	limits := wasm.ResourceLimits{MemoryPages: 4, FuelBudget: 10_000, WallClock: 3 * time.Second}
	err := h.rt.Dispatch(ctx, h.lm, export, "conformance-plugin", wasmHarnessNetScopes,
		h.deps, wasm.WASIConfig{}, limits, req, out)
	if err != nil {
		return ABIObservation{HasErr: true, ErrKind: errKindOf(err)}
	}
	data, mErr := json.Marshal(out)
	if mErr != nil {
		t.Fatalf("wasmHarness: marshal response: %v", mErr)
	}
	return ABIObservation{ResponseJSON: data}
}

// wasmRequestResponsePair returns a pointer pair (request, response) of
// the wasm.*Request/wasm.*Response types matching f.Method -- the same
// response types callRefSinkMethod returns, so JSON comparison is
// meaningful.
func wasmRequestResponsePair(f HostFnFixture) (req any, out any) {
	switch f.Method {
	case methodLog:
		return &wasm.LogRequest{}, &wasm.LogResponse{}
	case methodStorage:
		return &wasm.StorageRequest{}, &wasm.StorageResponse{}
	case methodHTTP:
		return &wasm.HTTPRequest{}, &wasm.HTTPResponse{}
	case methodStream:
		return &wasm.StreamRequest{}, &wasm.StreamResponse{}
	case methodSecretRef:
		return &wasm.SecretRefRequest{}, &wasm.SecretRefResponse{}
	case methodEventEmit:
		return &wasm.EventEmitRequest{}, &wasm.EventEmitResponse{}
	case methodToolRegister:
		return &wasm.ToolRegisterRequest{}, &wasm.ToolRegisterResponse{}
	default:
		return &struct{}{}, &struct{}{}
	}
}
