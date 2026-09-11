package wasm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
)

// Purpose: Runtime ties the pieces above together: wazero module
//
//	instantiation (task 1), WASI shims (task 2), the seven registered
//	host-ABI functions (tasks 4-6), and resource limits (task 3) into one
//	Dispatch entry point. One wazero store per dispatch: NewRuntime
//	builds a single wazero.Runtime with the host module registered once
//	(host functions read their per-call dependencies from ctx, not from
//	captured state), and Dispatch instantiates a fresh, anonymous
//	api.Module for every call — the ADR-N-S30-T6 spike's concurrency
//	constraint (fixed memory offsets unsafe across concurrent in-flight
//	calls on one instance) is satisfied structurally: no two calls ever
//	share an instance.

// Deps are the host-side dependencies host-ABI functions delegate to.
// Every field is required for a Dispatch call that exercises that
// function; a nil field whose function is actually invoked is a caller
// bug the corresponding handler surfaces as a KindInternal error rather
// than panicking (see each host_abi_*.go handler's missingDep guard,
// checked before the field is dereferenced).
type Deps struct {
	Logger  Logger
	Events  EventBus
	Stream  StreamSink
	Tools   ToolRegistrar
	Storage plugin.Storage
	Secrets SecretBroker
	Net     NetDoer
}

// callState is the per-Dispatch-call state the registered host
// functions read from ctx (they cannot take it as a constructor
// parameter — wazero's host module is built once in NewRuntime, before
// any call's Deps or plugin id are known).
type callState struct {
	pluginID  string
	netScopes []string
	deps      Deps

	// Fuel bookkeeping (resource_limits.go's spendFuel): fuelCancel is
	// nil for callState values built outside Dispatch (fuel disabled).
	fuelBudget uint64
	fuelSpent  uint64
	fuelCancel context.CancelCauseFunc
}

type callStateKey struct{}

func withCallState(ctx context.Context, cs *callState) context.Context {
	return context.WithValue(ctx, callStateKey{}, cs)
}

func callStateFrom(ctx context.Context) *callState {
	cs, _ := ctx.Value(callStateKey{}).(*callState)
	return cs
}

// Runtime is the wazero-backed WASM plugin runtime. The zero value is
// not usable; use NewRuntime.
type Runtime struct {
	rt               wazero.Runtime
	memoryLimitPages uint32
}

// NewRuntime builds a Runtime: a wazero.Runtime configured with
// CGO_ENABLED=0-safe defaults, WithCloseOnContextDone so an in-flight
// guest call actually aborts when Dispatch's context is canceled (the
// real enforcement point for both the fuel budget and the wall-clock
// cap below — neither is decorative), and the seven-function "env" host
// module registered once. memoryLimitPages is wazero's own
// WithMemoryLimitPages ceiling: a RUNTIME-wide setting in wazero's
// public API (v1.12.0), not a per-instantiation one — every module this
// Runtime ever instantiates shares this ceiling; Dispatch's
// ResourceLimits.MemoryPages (below) is validated against it rather
// than reconfiguring the engine per call. Callers needing genuinely
// different ceilings for different plugins construct separate Runtime
// values. Callers construct exactly one Runtime per process (or per
// isolation boundary) and call Close when done.
func NewRuntime(ctx context.Context, memoryLimitPages uint32) (*Runtime, error) {
	cfg := wazero.NewRuntimeConfig().WithCloseOnContextDone(true).WithMemoryLimitPages(memoryLimitPages)
	rt := wazero.NewRuntimeWithConfig(ctx, cfg)
	r := &Runtime{rt: rt, memoryLimitPages: memoryLimitPages}
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, rt); err != nil {
		_ = rt.Close(ctx)
		return nil, cascade.Wrapf(cascade.KindInternal, err, "wasm: failed to register wasi_snapshot_preview1")
	}
	if err := r.registerHostModule(ctx); err != nil {
		_ = rt.Close(ctx)
		return nil, cascade.Wrapf(cascade.KindInternal, err, "wasm: failed to register host module")
	}
	return r, nil
}

// registerHostModule builds the "env" host module once. Each Go closure
// reads its per-call callState from ctx (see hostLogFn et al.), not from
// a captured Deps value, since Deps varies per Dispatch call.
func (r *Runtime) registerHostModule(ctx context.Context) error {
	builder := r.rt.NewHostModuleBuilder("env")
	for _, hf := range []struct {
		export string
		fn     func(context.Context, api.Module, uint32, uint32) uint32
	}{
		{hostFnHTTP, dispatchFromCtx(hostHTTPFn)},
		{hostFnStorage, dispatchFromCtx(hostStorageFn)},
		{hostFnLog, dispatchFromCtx(hostLogFn)},
		{hostFnStream, dispatchFromCtx(hostStreamFn)},
		{hostFnSecretRef, dispatchFromCtx(hostSecretRefFn)},
		{hostFnEventEmit, dispatchFromCtx(hostEventEmitFn)},
		{hostFnToolRegister, dispatchFromCtx(hostToolRegisterFn)},
	} {
		builder = builder.NewFunctionBuilder().WithFunc(hf.fn).Export(hf.export)
	}
	_, err := builder.Instantiate(ctx)
	return err
}

// dispatchFromCtx adapts one of the host_*Fn builders (which take a
// *callState directly) into a wazero host function that reads its
// callState from ctx on every call, fails closed with a typed
// KindInternal result when Dispatch was not used to invoke it (no
// callState present) rather than dereferencing a nil pointer, and is
// this package's one chokepoint for every host-ABI call — the fuel
// budget (resource_limits.go) is spent here, once per call, before the
// call is allowed to proceed.
func dispatchFromCtx(build func(*callState) func(context.Context, api.Module, uint32, uint32) uint32) func(context.Context, api.Module, uint32, uint32) uint32 {
	return func(ctx context.Context, m api.Module, ptr, length uint32) uint32 {
		cs := callStateFrom(ctx)
		if cs == nil {
			return writeErr(m, cascade.New(cascade.KindInternal, "wasm: host function invoked outside Dispatch (no call state)"))
		}
		if err := cs.spendFuel(); err != nil {
			return writeErr(m, err)
		}
		return build(cs)(ctx, m, ptr, length)
	}
}

// Loader returns a ModuleLoader bound to this Runtime.
func (r *Runtime) Loader() *ModuleLoader { return NewModuleLoader(r) }

// WASIConfig is task 2's WASI snapshot_preview1 subset: stdio
// pass-through for log capture, args/env stubbed to manifest-declared
// values only (never the ambient host process's own environment — a
// zero-value Env map means the guest sees NO env vars at all, not the
// host's), and a seeded deterministic random source. Every field is
// optional; a zero WASIConfig gives the guest no args, no env, discarded
// stdio, and a fixed-seed random stream.
type WASIConfig struct {
	Args     []string
	Env      map[string]string
	Stdout   io.Writer
	Stderr   io.Writer
	RandSeed uint64
}

// apply configures modCfg per WASIConfig: args and env come ONLY from
// the manifest-declared values here, never from the host process's own
// os.Environ (Dispatch never reads it, so there is nothing to leak).
// Monotonic clock access is granted via WithSysNanotime; wall-clock is
// deliberately NOT granted (WithSysWalltime), keeping guest time access
// monotonic-only per the task.
func (w WASIConfig) apply(cfg wazero.ModuleConfig) wazero.ModuleConfig {
	cfg = cfg.WithArgs(w.Args...).WithSysNanotime().WithRandSource(newSeededRandReader(w.RandSeed))
	for k, v := range w.Env {
		cfg = cfg.WithEnv(k, v)
	}
	if w.Stdout != nil {
		cfg = cfg.WithStdout(w.Stdout)
	}
	if w.Stderr != nil {
		cfg = cfg.WithStderr(w.Stderr)
	}
	return cfg
}

// seededRandReader is a small, deterministic xorshift64* io.Reader: WASI
// "seeded random" needs reproducible output across runs, which the
// repo's math/rand-outside-tests gate (forbidigo) forbids reaching for,
// so this is a self-contained, non-cryptographic PRNG local to this
// file rather than an import of math/rand.
type seededRandReader struct{ state uint64 }

func newSeededRandReader(seed uint64) *seededRandReader {
	if seed == 0 {
		seed = 1 // a zero state never advances under xorshift.
	}
	return &seededRandReader{state: seed}
}

func (s *seededRandReader) next() uint64 {
	s.state ^= s.state << 13
	s.state ^= s.state >> 7
	s.state ^= s.state << 17
	return s.state * 2685821657736338717
}

// Read implements io.Reader, filling p with xorshift64* output. Always
// returns len(p), nil.
func (s *seededRandReader) Read(p []byte) (int, error) {
	for i := 0; i < len(p); i += 8 {
		v := s.next()
		for j := 0; j < 8 && i+j < len(p); j++ {
			p[i+j] = byte(v >> (8 * j))
		}
	}
	return len(p), nil
}

// Dispatch instantiates a fresh, isolated instance of lm, writes req as
// the request payload for guestExport, calls it, and decodes the
// response into out. netScopes gates host_http; deps and limits are
// required (a zero ResourceLimits value refuses via wrapLoadFailed-style
// validation below, matching "never a panic, never a silent
// degradation").
func (r *Runtime) Dispatch(ctx context.Context, lm *LoadedModule, guestExport, pluginID string, netScopes []string, deps Deps, wasi WASIConfig, limits ResourceLimits, req any, out any) error {
	if lm == nil {
		return cascade.New(cascade.KindInvalidInput, "wasm: Dispatch: nil loaded module")
	}
	if limits.WallClock <= 0 {
		return cascade.New(cascade.KindInvalidInput, "wasm: Dispatch: ResourceLimits.WallClock must be positive")
	}
	if limits.MemoryPages > r.memoryLimitPages {
		return wrapMemoryLimit(r.memoryLimitPages)
	}

	ctx, cancel, cancelFuel := withResourceLimits(ctx, limits)
	defer cancel()
	ctx = withCallState(ctx, &callState{
		pluginID: pluginID, netScopes: netScopes, deps: deps,
		fuelBudget: limits.FuelBudget, fuelCancel: cancelFuel,
	})

	modCfg := wasi.apply(wazero.NewModuleConfig().WithName(""))
	mod, err := r.rt.InstantiateModule(ctx, lm.compiled, modCfg)
	if err != nil {
		return classifyLimitErr(ctx, cascade.Wrapf(cascade.KindInvalidInput, err, "wasm: failed to instantiate module %q", lm.id))
	}
	defer func() { _ = mod.Close(context.WithoutCancel(ctx)) }()

	return dispatchCall(ctx, mod, guestExport, req, out)
}

// dispatchCall performs the write-request/call/read-response/decode
// sequence shared by every Dispatch invocation.
func dispatchCall(ctx context.Context, mod api.Module, guestExport string, req, out any) error {
	fn := mod.ExportedFunction(guestExport)
	if fn == nil {
		return cascade.Newf(cascade.KindInvalidInput, "wasm: module has no exported function %q", guestExport)
	}
	data, err := json.Marshal(req)
	if err != nil {
		return cascade.Wrapf(cascade.KindInvalidInput, err, "wasm: failed to marshal request")
	}
	if !mod.Memory().Write(wasmReqOffset, data) {
		return cascade.New(cascade.KindInvalidInput, "wasm: failed to write request into module memory")
	}

	results, err := fn.Call(ctx, uint64(wasmReqOffset), uint64(len(data)))
	if err != nil {
		return classifyLimitErr(ctx, cascade.Wrapf(cascade.KindUnavailable, err, "wasm: guest call to %q failed", guestExport))
	}

	respLen := uint32(results[0])
	respBytes, ok := mod.Memory().Read(wasmRespOffset, respLen)
	if !ok {
		return cascade.New(cascade.KindInvalidInput, "wasm: failed to read response from module memory")
	}
	var res result
	if err := json.Unmarshal(respBytes, &res); err != nil {
		return cascade.Wrapf(cascade.KindInvalidInput, err, "wasm: response envelope is not valid JSON")
	}
	if res.Error != "" {
		return fmt.Errorf("%s", res.Error) //nolint:err113 // guest-reported error string, not a Go sentinel.
	}
	if out != nil && len(res.Payload) > 0 {
		if err := json.Unmarshal(res.Payload, out); err != nil {
			return cascade.Wrapf(cascade.KindInvalidInput, err, "wasm: response payload is not valid JSON")
		}
	}
	return nil
}

// Close releases the Runtime's underlying wazero.Runtime and every
// module it compiled.
func (r *Runtime) Close(ctx context.Context) error { return r.rt.Close(ctx) }
