package wasm

import (
	"context"
	"errors"
	"sync"

	"github.com/tetratelabs/wazero"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose: ModuleLoader's load-validate-cache flow (task 1): compile a
//
//	guest module's bytes once, validate its host-ABI version handshake
//	(task 7) against a throwaway instance, and cache the *compiled*
//	module (never an instance) so Runtime.Dispatch can cheaply
//	re-instantiate a fresh, isolated instance per call.
//
// Constraints: a compiled module's exported global initializer is only
//
//	observable after instantiation in wazero's public API (there is no
//	pre-instantiation export-value introspection), so checkABIVersion
//	instantiates once, reads the global, and closes that instance
//	immediately — the cached artifact is strictly the CompiledModule.

// ErrABIVersionMismatch reports a guest module whose exported
// "cascade_abi_version" global does not equal HostABIVersionV1.
var ErrABIVersionMismatch = errors.New("wasm: module host-ABI version mismatch")

func wrapABIMismatch(got int) error {
	return cascade.Wrapf(cascade.KindUnsupported, ErrABIVersionMismatch,
		"wasm: module reports host-ABI version %d, host requires %d", got, HostABIVersionV1)
}

func wrapLoadFailed(cause error) error {
	return cascade.Wrapf(cascade.KindInvalidInput, cause, "wasm: failed to compile module")
}

// LoadedModule is a cached, validated, compiled guest module. It carries
// no per-call state — Runtime.Dispatch instantiates a fresh api.Module
// from it on every call.
type LoadedModule struct {
	id       string
	compiled wazero.CompiledModule
}

// ModuleLoader loads, validates, and caches compiled WASM modules by id.
// The zero value is not usable; use NewModuleLoader.
type ModuleLoader struct {
	rt *Runtime

	mu    sync.RWMutex
	cache map[string]*LoadedModule
}

// NewModuleLoader returns a ModuleLoader bound to rt, whose wazero
// runtime and registered host module Load's validation instance uses.
func NewModuleLoader(rt *Runtime) *ModuleLoader {
	return &ModuleLoader{rt: rt, cache: make(map[string]*LoadedModule)}
}

// Load compiles wasmBytes, validates its host-ABI version handshake, and
// caches the result under id, replacing any prior entry for the same id.
// A module whose "cascade_abi_version" global is absent is accepted
// (unversioned guests default to v1 for backward compatibility with the
// O/S-30.T6 conformance fixtures, which predate this global); a module
// whose global is present and does not equal HostABIVersionV1 is a hard
// typed error — Load never accepts it, never degrades silently.
//
// A wazero CompiledModule holds native-side resources that only wazero's
// own Close releases; a compiled-but-rejected module (ABI mismatch) and
// a replaced cache entry's PREVIOUS compiled module are both closed
// below rather than left for the garbage collector, which never runs
// wazero's own cleanup. FuzzLoadModule (loader_test.go), which calls
// Load repeatedly under the SAME id with malformed and ABI-mismatched
// input, is what surfaced this: without these two Close calls, native
// memory accumulates across the fuzz run's 100k+ iterations until the
// worker process is killed for exhausting memory — a real find, not a
// hypothetical one (see the journal for this ticket).
func (l *ModuleLoader) Load(ctx context.Context, id string, wasmBytes []byte) (*LoadedModule, error) {
	compiled, err := l.rt.rt.CompileModule(ctx, wasmBytes)
	if err != nil {
		return nil, wrapLoadFailed(err)
	}
	if err := l.checkABIVersion(ctx, compiled); err != nil {
		_ = compiled.Close(ctx)
		return nil, err
	}

	lm := &LoadedModule{id: id, compiled: compiled}
	l.mu.Lock()
	prev := l.cache[id]
	l.cache[id] = lm
	l.mu.Unlock()
	if prev != nil {
		_ = prev.compiled.Close(ctx)
	}
	return lm, nil
}

// checkABIVersion instantiates compiled anonymously, reads its exported
// "cascade_abi_version" global if present, and closes the instance.
func (l *ModuleLoader) checkABIVersion(ctx context.Context, compiled wazero.CompiledModule) error {
	mod, err := l.rt.rt.InstantiateModule(ctx, compiled, wazero.NewModuleConfig().WithName(""))
	if err != nil {
		return wrapLoadFailed(err)
	}
	defer func() { _ = mod.Close(ctx) }()

	g := mod.ExportedGlobal(abiVersionGlobal)
	if g == nil {
		return nil // unversioned guest: accepted for backward compatibility.
	}
	if got := int(int32(g.Get())); got != HostABIVersionV1 {
		return wrapABIMismatch(got)
	}
	return nil
}

// Get returns the cached module for id, or (nil, false) if Load has not
// been called for it (or it was never loaded).
func (l *ModuleLoader) Get(id string) (*LoadedModule, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	lm, ok := l.cache[id]
	return lm, ok
}
