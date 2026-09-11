package wasm

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

// instantiateResourceFixture instantiates testdata/resource.wasm with
// limits and a callState wired, for direct (non-envelope) export calls.
func instantiateResourceFixture(ctx context.Context, t *testing.T, rt *Runtime, limits ResourceLimits) (context.Context, api.Module, context.CancelFunc) {
	t.Helper()
	lm, err := rt.Loader().Load(ctx, "resource-"+t.Name(), readFixture(t, "resource.wasm"))
	if err != nil {
		t.Fatalf("Load resource.wasm: %v", err)
	}
	rctx, cancel, cancelFuel := withResourceLimits(ctx, limits)
	rctx = withCallState(rctx, &callState{pluginID: "p", deps: testDeps(), fuelBudget: limits.FuelBudget, fuelCancel: cancelFuel})
	modCfg := WASIConfig{}.apply(wazero.NewModuleConfig().WithName(""))
	mod, err := rt.rt.InstantiateModule(rctx, lm.compiled, modCfg)
	if err != nil {
		cancel()
		t.Fatalf("InstantiateModule: %v", err)
	}
	return rctx, mod, cancel
}

// TestResourceLimits_Exhaustion covers all three exhaustion paths named
// by the acceptance criterion: memory ceiling, fuel exhaustion, and
// wall-clock cap. Each must terminate with a structured typed error;
// none may panic or silently truncate. Split into one top-level test per
// path (funlen) rather than one function holding all three t.Run
// subtests.
func TestResourceLimits_Exhaustion(t *testing.T) {
	ctx := context.Background()
	t.Run("memory ceiling", func(t *testing.T) { testResourceLimitsMemoryCeiling(ctx, t) })
	t.Run("fuel exhaustion", func(t *testing.T) { testResourceLimitsFuelExhaustion(ctx, t) })
	t.Run("wall clock", func(t *testing.T) { testResourceLimitsWallClock(ctx, t) })
}

func testResourceLimitsMemoryCeiling(ctx context.Context, t *testing.T) {
	// A tightly-capped Runtime of its own: WithMemoryLimitPages is a
	// runtime-wide wazero setting (v1.12.0's public API has no
	// per-instantiation override), so this subtest cannot share
	// mustNewRuntime's generous default ceiling.
	rt, err := NewRuntime(ctx, 2)
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close(ctx) })
	limits := ResourceLimits{MemoryPages: 2, FuelBudget: 1000, WallClock: 2 * time.Second}
	rctx, mod, cancel := instantiateResourceFixture(ctx, t, rt, limits)
	defer cancel()
	defer func() { _ = mod.Close(ctx) }()

	fn := mod.ExportedFunction("grow_memory")
	results, err := fn.Call(rctx, uint64(uint32(4000))) // far beyond the 2-page ceiling.
	if err != nil {
		t.Fatalf("grow_memory call itself errored: %v", err)
	}
	if mapErr := mapGrowResult(int32(int64(results[0])), limits); mapErr == nil {
		t.Fatal("expected ErrMemoryLimitExceeded from a grow beyond the ceiling")
	} else if !errors.Is(mapErr, ErrMemoryLimitExceeded) {
		t.Fatalf("mapGrowResult = %v, want ErrMemoryLimitExceeded", mapErr)
	}
}

func testResourceLimitsFuelExhaustion(ctx context.Context, t *testing.T) {
	rt := mustNewRuntime(ctx, t)
	limits := ResourceLimits{MemoryPages: 4, FuelBudget: 5, WallClock: 5 * time.Second}
	rctx, mod, cancel := instantiateResourceFixture(ctx, t, rt, limits)
	defer cancel()
	defer func() { _ = mod.Close(ctx) }()

	fn := mod.ExportedFunction("spin_fuel")
	_, err := fn.Call(rctx, uint64(uint32(1_000_000))) // far more calls than the budget.
	if err == nil {
		t.Fatal("expected the fuel budget to abort the call")
	}
	mapped := classifyLimitErr(rctx, err)
	if !errors.Is(mapped, ErrFuelExhausted) {
		t.Fatalf("classifyLimitErr = %v, want ErrFuelExhausted", mapped)
	}
}

func testResourceLimitsWallClock(ctx context.Context, t *testing.T) {
	rt := mustNewRuntime(ctx, t)
	limits := ResourceLimits{MemoryPages: 4, FuelBudget: 1_000_000_000, WallClock: 50 * time.Millisecond}
	rctx, mod, cancel := instantiateResourceFixture(ctx, t, rt, limits)
	defer cancel()
	defer func() { _ = mod.Close(ctx) }()

	fn := mod.ExportedFunction("spin_forever")
	_, err := fn.Call(rctx)
	if err == nil {
		t.Fatal("expected the wall-clock cap to abort an infinite loop")
	}
	mapped := classifyLimitErr(rctx, err)
	if !errors.Is(mapped, ErrWallClockExceeded) {
		t.Fatalf("classifyLimitErr = %v, want ErrWallClockExceeded", mapped)
	}
}

func TestDefaultResourceLimits(t *testing.T) {
	d := DefaultResourceLimits()
	if d.MemoryPages == 0 || d.FuelBudget == 0 || d.WallClock <= 0 {
		t.Fatalf("DefaultResourceLimits returned a zero field: %+v", d)
	}
}

func TestClassifyLimitErr_NilPassthrough(t *testing.T) {
	if classifyLimitErr(context.Background(), nil) != nil {
		t.Fatal("expected nil passthrough")
	}
}

// TestClassifyLimitErr_UnrelatedCancellation proves classifyLimitErr
// returns err UNCHANGED (rather than substituting a resource-limit
// sentinel) when ctx's cancellation cause is ordinary
// context.Canceled/DeadlineExceeded, not one of this package's own
// typed exhaustion errors -- the negative case of the classification
// this function performs on every Dispatch-call failure.
func TestClassifyLimitErr_UnrelatedCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	original := errors.New("some other call failure")
	if got := classifyLimitErr(ctx, original); got != original {
		t.Fatalf("classifyLimitErr = %v, want the original error unchanged", got)
	}
}

// TestMapGrowResult_WithinLimit proves the non-exceeded branch: a
// memory.grow return value >= 0 (the guest's successful new page count,
// standard WASM semantics) is not an error.
func TestMapGrowResult_WithinLimit(t *testing.T) {
	if err := mapGrowResult(4, ResourceLimits{MemoryPages: 16}); err != nil {
		t.Fatalf("mapGrowResult(4, ...) = %v, want nil", err)
	}
}

// TestSpendFuel_UnlimitedWithoutFuelCancel proves a callState built
// outside Dispatch (fuelCancel nil, per its doc comment -- used only by
// tests that bypass Dispatch) treats fuel as unlimited rather than
// panicking on the nil CancelCauseFunc.
func TestSpendFuel_UnlimitedWithoutFuelCancel(t *testing.T) {
	cs := &callState{pluginID: "p", deps: testDeps()}
	for range 3 {
		if err := cs.spendFuel(); err != nil {
			t.Fatalf("spendFuel with no fuelCancel = %v, want nil (unlimited)", err)
		}
	}
}
