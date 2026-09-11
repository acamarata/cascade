package wasm

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/tetratelabs/wazero"
)

// TestHostAPI drives host_log, host_stream, host_toolregister, and
// host_eventemit through the real WASM call boundary (fixture.wasm's
// call_host_* wrappers), asserting each forwards into the corresponding
// Deps seam. Split into one top-level test per method (funlen) rather
// than one function holding all four t.Run subtests.
func TestHostAPI(t *testing.T) {
	ctx := context.Background()
	rt := mustNewRuntime(ctx, t)
	lm, err := rt.Loader().Load(ctx, "core", readFixture(t, "fixture.wasm"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	t.Run("host_log", func(t *testing.T) { testHostAPILog(ctx, t, rt, lm) })
	t.Run("host_stream", func(t *testing.T) { testHostAPIStream(ctx, t, rt, lm) })
	t.Run("host_toolregister", func(t *testing.T) { testHostAPIToolRegister(ctx, t, rt, lm) })
	t.Run("host_eventemit", func(t *testing.T) { testHostAPIEventEmit(ctx, t, rt, lm) })
}

func testHostAPILog(ctx context.Context, t *testing.T, rt *Runtime, lm *LoadedModule) {
	logger := &fakeLogger{}
	deps := testDeps()
	deps.Logger = logger
	var resp LogResponse
	if err := rt.Dispatch(ctx, lm, "call_host_log", "p", nil, deps, WASIConfig{}, testLimits(),
		LogRequest{Level: "warn", Message: "m1"}, &resp); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if len(logger.lines) != 1 || logger.lines[0] != "warn: m1" {
		t.Fatalf("logger.lines = %v", logger.lines)
	}
}

func testHostAPIStream(ctx context.Context, t *testing.T, rt *Runtime, lm *LoadedModule) {
	stream := &fakeStream{}
	deps := testDeps()
	deps.Stream = stream
	var resp StreamResponse
	if err := rt.Dispatch(ctx, lm, "call_host_stream", "p", nil, deps, WASIConfig{}, testLimits(),
		StreamRequest{ChannelID: "c1", Data: []byte("abc")}, &resp); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if resp.BytesWritten != 3 || len(stream.chunks) != 1 {
		t.Fatalf("resp=%+v chunks=%v", resp, stream.chunks)
	}
}

func testHostAPIToolRegister(ctx context.Context, t *testing.T, rt *Runtime, lm *LoadedModule) {
	tools := &fakeTools{}
	deps := testDeps()
	deps.Tools = tools
	var resp ToolRegisterResponse
	if err := rt.Dispatch(ctx, lm, "call_host_toolregister", "p", nil, deps, WASIConfig{}, testLimits(),
		ToolRegisterRequest{ToolName: "t1", Schema: []byte("{}")}, &resp); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if !resp.Registered || len(tools.names) != 1 || tools.names[0] != "t1" {
		t.Fatalf("resp=%+v names=%v", resp, tools.names)
	}
}

func testHostAPIEventEmit(ctx context.Context, t *testing.T, rt *Runtime, lm *LoadedModule) {
	events := &fakeEvents{}
	deps := testDeps()
	deps.Events = events
	var resp EventEmitResponse
	if err := rt.Dispatch(ctx, lm, "call_host_eventemit", "p", nil, deps, WASIConfig{}, testLimits(),
		EventEmitRequest{Topic: "t", Payload: []byte("x")}, &resp); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if resp.EventID == "" || len(events.topics) != 1 {
		t.Fatalf("resp=%+v topics=%v", resp, events.topics)
	}
}

// TestHostAPI_DepsErrorPropagation proves each core handler propagates
// its Deps seam's own error rather than swallowing it or succeeding
// anyway.
func TestHostAPI_DepsErrorPropagation(t *testing.T) {
	ctx := context.Background()
	rt := mustNewRuntime(ctx, t)
	lm, err := rt.Loader().Load(ctx, "core-err", readFixture(t, "fixture.wasm"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	cases := []struct {
		name    string
		export  string
		mutate  func(*Deps)
		req     any
		wantErr bool
	}{
		{"host_log", "call_host_log", func(d *Deps) { d.Logger = errLogger{} }, LogRequest{Level: "x", Message: "y"}, true},
		{"host_stream", "call_host_stream", func(d *Deps) { d.Stream = errStream{} }, StreamRequest{ChannelID: "c"}, true},
		{"host_toolregister", "call_host_toolregister", func(d *Deps) { d.Tools = errTools{} }, ToolRegisterRequest{ToolName: "t"}, true},
		{"host_eventemit", "call_host_eventemit", func(d *Deps) { d.Events = errEvents{} }, EventEmitRequest{Topic: "t"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			deps := testDeps()
			c.mutate(&deps)
			err := rt.Dispatch(ctx, lm, c.export, "p", nil, deps, WASIConfig{}, testLimits(), c.req, nil)
			if (err != nil) != c.wantErr {
				t.Fatalf("Dispatch err=%v, wantErr=%v", err, c.wantErr)
			}
		})
	}
}

// TestDispatchFromCtx_NoCallState proves the defensive nil-callState
// guard: a host function invoked without Dispatch's ctx wiring fails
// closed with a typed result rather than dereferencing a nil pointer.
func TestDispatchFromCtx_NoCallState(t *testing.T) {
	ctx := context.Background()
	rt := mustNewRuntime(ctx, t)
	lm, err := rt.Loader().Load(ctx, "nocallstate", readFixture(t, "fixture.wasm"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	modCfg := WASIConfig{}.apply(wazero.NewModuleConfig().WithName(""))
	mod, err := rt.rt.InstantiateModule(ctx, lm.compiled, modCfg) // note: bare ctx, no callState.
	if err != nil {
		t.Fatalf("InstantiateModule: %v", err)
	}
	defer func() { _ = mod.Close(ctx) }()

	data, _ := json.Marshal(LogRequest{Level: "info", Message: "x"})
	mod.Memory().Write(wasmReqOffset, data)
	fn := mod.ExportedFunction("call_host_log")
	results, err := fn.Call(ctx, uint64(wasmReqOffset), uint64(len(data)))
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	respBytes, _ := mod.Memory().Read(wasmRespOffset, uint32(results[0]))
	var res result
	_ = json.Unmarshal(respBytes, &res)
	if res.Error == "" {
		t.Fatal("expected a typed error when no callState is present")
	}
}

// TestHostAPI_BadPointer proves a bad pointer/length pair fails closed
// with a typed error rather than a panic: calling an exported wrapper
// with an out-of-bounds request offset. Table-driven across all seven
// host-ABI exports so each handler's own readRequest failure branch (not
// just host_log's) is exercised.
func TestHostAPI_BadPointer(t *testing.T) {
	ctx := context.Background()
	rt := mustNewRuntime(ctx, t)

	exports := []string{
		"call_host_log", "call_host_stream", "call_host_toolregister",
		"call_host_eventemit", "call_host_storage", "call_host_secretref", "call_host_http",
	}
	for _, export := range exports {
		t.Run(export, func(t *testing.T) {
			res := badPointerResult(ctx, t, rt, export)
			if res.Error == "" {
				t.Fatalf("expected result.Error to be set for a bad pointer, got zero value")
			}
		})
	}
}

// badPointerResult loads fixture.wasm fresh, calls export with an
// out-of-bounds request offset (far beyond the module's 2-page linear
// memory), and decodes the written result envelope. The wasm call
// itself must never error (the host fn reports failure in-band via the
// envelope, per readRequest's contract), and must never panic.
func badPointerResult(ctx context.Context, t *testing.T, rt *Runtime, export string) result {
	t.Helper()
	lm, err := rt.Loader().Load(ctx, "badptr-"+export, readFixture(t, "fixture.wasm"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	deps := testDeps()
	limits := testLimits()

	rctx, cancel, cancelFuel := withResourceLimits(ctx, limits)
	defer cancel()
	rctx = withCallState(rctx, &callState{pluginID: "p", netScopes: []string{"allowed.example.com"}, deps: deps, fuelBudget: limits.FuelBudget, fuelCancel: cancelFuel})
	modCfg := WASIConfig{}.apply(wazero.NewModuleConfig().WithName(""))
	mod, err := rt.rt.InstantiateModule(rctx, lm.compiled, modCfg)
	if err != nil {
		t.Fatalf("InstantiateModule: %v", err)
	}
	defer func() { _ = mod.Close(ctx) }()

	fn := mod.ExportedFunction(export)
	results, err := fn.Call(rctx, uint64(10_000_000), uint64(16))
	if err != nil {
		t.Fatalf("Call itself should not error (the host fn reports failure in-band): %v", err)
	}
	respLen := uint32(results[0])
	respBytes, ok := mod.Memory().Read(wasmRespOffset, respLen)
	if !ok || respLen == 0 {
		t.Fatal("expected a written error result, got none")
	}
	var res result
	if err := json.Unmarshal(respBytes, &res); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	return res
}

// TestHostAPI_MissingDep proves every host-ABI handler fails closed with
// a typed result-envelope error, never a nil-pointer panic, when
// Dispatch is called with a Deps value whose corresponding field was
// left nil (a caller bug the missingDep guard in host_abi_core.go, and
// its per-file call sites, exist to catch). If any guard were missing,
// the handler would dereference a nil interface and the whole test
// binary would crash rather than report a failing subtest — the guard's
// existence is directly load-bearing for this test completing at all.
func TestHostAPI_MissingDep(t *testing.T) {
	ctx := context.Background()
	rt := mustNewRuntime(ctx, t)
	lm, err := rt.Loader().Load(ctx, "missingdep", readFixture(t, "fixture.wasm"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	cases := []struct {
		name   string
		export string
		mutate func(*Deps)
		req    any
	}{
		{"host_log", "call_host_log", func(d *Deps) { d.Logger = nil }, LogRequest{Level: "info", Message: "m"}},
		{"host_stream", "call_host_stream", func(d *Deps) { d.Stream = nil }, StreamRequest{ChannelID: "c", Data: []byte("x")}},
		{"host_toolregister", "call_host_toolregister", func(d *Deps) { d.Tools = nil }, ToolRegisterRequest{ToolName: "t", Schema: []byte("{}")}},
		{"host_eventemit", "call_host_eventemit", func(d *Deps) { d.Events = nil }, EventEmitRequest{Topic: "t", Payload: []byte("{}")}},
		{"host_storage", "call_host_storage", func(d *Deps) { d.Storage = nil }, StorageRequest{Op: "get", Key: "k"}},
		{"host_secretref", "call_host_secretref", func(d *Deps) { d.Secrets = nil }, SecretRefRequest{Name: "n"}},
		{"host_http", "call_host_http", func(d *Deps) { d.Net = nil }, HTTPRequest{Method: "GET", URL: "https://allowed.example.com"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			deps := testDeps()
			c.mutate(&deps)
			err := rt.Dispatch(ctx, lm, c.export, "p", []string{"allowed.example.com"}, deps, WASIConfig{}, testLimits(), c.req, nil)
			if err == nil {
				t.Fatalf("%s with nil dep: expected a typed error, got nil", c.name)
			}
		})
	}
}
