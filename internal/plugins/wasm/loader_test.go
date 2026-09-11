package wasm

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestModuleLoader_LoadAndGet(t *testing.T) {
	ctx := context.Background()
	rt := mustNewRuntime(ctx, t)
	loader := rt.Loader()

	if _, ok := loader.Get("missing"); ok {
		t.Fatal("expected Get to report not-found before any Load")
	}

	lm, err := loader.Load(ctx, "m1", readFixture(t, "fixture.wasm"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if lm.id != "m1" {
		t.Fatalf("id = %q, want m1", lm.id)
	}
	got, ok := loader.Get("m1")
	if !ok || got != lm {
		t.Fatalf("Get after Load: got=%v ok=%v", got, ok)
	}
}

func TestModuleLoader_Load_InvalidBytes(t *testing.T) {
	ctx := context.Background()
	rt := mustNewRuntime(ctx, t)
	_, err := rt.Loader().Load(ctx, "bad", []byte("not a wasm module"))
	if err == nil {
		t.Fatal("expected an error compiling malformed bytes")
	}
}

func TestModuleLoader_Load_ReplacesEntry(t *testing.T) {
	ctx := context.Background()
	rt := mustNewRuntime(ctx, t)
	loader := rt.Loader()

	first, err := loader.Load(ctx, "same-id", readFixture(t, "fixture.wasm"))
	if err != nil {
		t.Fatalf("Load first: %v", err)
	}
	second, err := loader.Load(ctx, "same-id", readFixture(t, "fixture.wasm"))
	if err != nil {
		t.Fatalf("Load second: %v", err)
	}
	got, ok := loader.Get("same-id")
	if !ok || got != second || got == first {
		t.Fatalf("expected the second Load to replace the cached entry")
	}
}

// FuzzLoadModule seeds with a real, valid WASM binary
// (testdata/fuzz/FuzzLoadModule/seed001, a copy of fixture.wasm) and
// asserts Load never panics on arbitrary byte mutations — it either
// accepts a well-formed module or returns a typed error, per §5.7.
func FuzzLoadModule(f *testing.F) {
	// testdata/fuzz/FuzzLoadModule/seed001 (a Go fuzz corpus-encoded copy
	// of fixture.wasm, per §5.7's seed-corpus requirement) is loaded
	// automatically by the fuzzing framework from that directory; f.Add
	// below seeds additional cases directly, from the real binary on
	// disk plus two malformed edge cases.
	f.Add(readFuzzSeedFixture(f))
	f.Add([]byte{})
	f.Add([]byte("not wasm at all"))

	ctx := context.Background()
	rt, err := NewRuntime(ctx, testMemoryLimitPages)
	if err != nil {
		f.Fatalf("NewRuntime: %v", err)
	}
	f.Cleanup(func() { _ = rt.Close(ctx) })
	loader := rt.Loader()

	f.Fuzz(func(_ *testing.T, data []byte) {
		// Load must never panic; any outcome (success or typed error) is
		// acceptable for arbitrary fuzzed input.
		_, _ = loader.Load(ctx, "fuzz", data)
	})
}

// readFuzzSeedFixture reads the real fixture.wasm binary directly (not
// the Go fuzz corpus-encoded copy at testdata/fuzz/FuzzLoadModule/, which
// the fuzzing framework parses on its own).
func readFuzzSeedFixture(f *testing.F) []byte {
	f.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "fixture.wasm"))
	if err != nil {
		f.Fatalf("read fixture.wasm: %v", err)
	}
	return data
}
