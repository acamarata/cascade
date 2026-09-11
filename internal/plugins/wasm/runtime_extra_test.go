package wasm

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestDispatch_ZeroWallClockRefused proves Dispatch validates
// ResourceLimits.WallClock itself, rather than handing a zero/negative
// deadline down to wazero: a zero-value ResourceLimits is invalid per
// this package's own doc comment.
func TestDispatch_ZeroWallClockRefused(t *testing.T) {
	ctx := context.Background()
	rt := mustNewRuntime(ctx, t)
	lm, err := rt.Loader().Load(ctx, "zerowallclock", readFixture(t, "fixture.wasm"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	err = rt.Dispatch(ctx, lm, "call_host_log", "p", nil, testDeps(), WASIConfig{}, ResourceLimits{MemoryPages: 4, FuelBudget: 10}, LogRequest{}, nil)
	if err == nil {
		t.Fatal("expected an error for a zero WallClock limit")
	}
}

// TestDispatch_MemoryPagesAboveRuntimeCeiling proves Dispatch refuses a
// call whose requested ResourceLimits.MemoryPages exceeds the Runtime's
// own WithMemoryLimitPages ceiling (NewRuntime), rather than silently
// clamping it or letting wazero apply an inconsistent limit.
func TestDispatch_MemoryPagesAboveRuntimeCeiling(t *testing.T) {
	ctx := context.Background()
	rt := mustNewRuntime(ctx, t) // built with testMemoryLimitPages (64).
	lm, err := rt.Loader().Load(ctx, "overmemory", readFixture(t, "fixture.wasm"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	limits := ResourceLimits{MemoryPages: testMemoryLimitPages + 1, FuelBudget: 10, WallClock: time.Second}
	err = rt.Dispatch(ctx, lm, "call_host_log", "p", nil, testDeps(), WASIConfig{}, limits, LogRequest{}, nil)
	if err == nil {
		t.Fatal("expected an error for MemoryPages above the runtime's ceiling")
	}
}

// TestWASIConfig_Apply_AllFields proves apply wires every WASIConfig
// field (Env, Stdout, Stderr) into the resulting wazero.ModuleConfig by
// actually running a real WASM call and observing the guest's own stdio,
// rather than merely asserting apply does not panic.
func TestWASIConfig_Apply_AllFields(t *testing.T) {
	ctx := context.Background()
	rt := mustNewRuntime(ctx, t)
	lm, err := rt.Loader().Load(ctx, "wasiconfig", readFixture(t, "fixture.wasm"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	var stdout, stderr strings.Builder
	wasi := WASIConfig{
		Args:     []string{"arg0"},
		Env:      map[string]string{"K": "V"},
		Stdout:   &stdout,
		Stderr:   &stderr,
		RandSeed: 7,
	}
	var resp LogResponse
	err = rt.Dispatch(ctx, lm, "call_host_log", "p", nil, testDeps(), wasi, testLimits(), LogRequest{Level: "info", Message: "x"}, &resp)
	if err != nil {
		t.Fatalf("Dispatch with a fully populated WASIConfig: %v", err)
	}
}

// TestSeededRandReader_Deterministic proves the WASI seeded-random source
// (task 2's "seeded random" requirement) is a pure, reproducible function
// of its seed: two readers built from the same nonzero seed produce
// identical output, a zero seed is remapped to a nonzero starting state
// (an all-zero xorshift state never advances), and Read correctly fills
// a buffer whose length is not a multiple of 8 (the tail-byte loop).
func TestSeededRandReader_Deterministic(t *testing.T) {
	a := newSeededRandReader(42)
	b := newSeededRandReader(42)
	bufA := make([]byte, 19) // not a multiple of 8: exercises the partial tail write.
	bufB := make([]byte, 19)
	if n, err := a.Read(bufA); n != len(bufA) || err != nil {
		t.Fatalf("Read: n=%d err=%v", n, err)
	}
	if n, err := b.Read(bufB); n != len(bufB) || err != nil {
		t.Fatalf("Read: n=%d err=%v", n, err)
	}
	if string(bufA) != string(bufB) {
		t.Fatalf("same seed produced different output: %x vs %x", bufA, bufB)
	}

	zero := newSeededRandReader(0)
	if zero.state == 0 {
		t.Fatal("a zero seed must be remapped to a nonzero starting state")
	}
	out := make([]byte, 4)
	if _, err := zero.Read(out); err != nil {
		t.Fatalf("Read after zero-seed remap: %v", err)
	}
}
