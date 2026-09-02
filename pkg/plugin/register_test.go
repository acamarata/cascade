package plugin

import (
	"context"
	"fmt"
	"sync"
	"testing"
)

// Purpose: unit tests for the compile-time builtin registration surface
//   (register.go): grants seeding, freeze-on-first-snapshot semantics,
//   deterministic (id-sorted) ordering, and concurrency safety. Package
//   plugin (white-box), not plugin_test, so tests can construct private
//   builtinRegistry instances directly rather than perturbing the single
//   process-global defaultBuiltinRegistry that ExampleRegisterBuiltin below
//   exercises for real.
// Constraints: Art.7.1 — no writes anywhere, this package performs none;
//   fakeHandlers is a BuiltinHandlers test fake confined to this _test.go
//   file, never a non-test file (Art.1).
// SPORT: pkg/plugin builtin-registration-api tests (ADD) — P1-E03-W1-S05-T7.

// fakeHandlers is a minimal BuiltinHandlers test fake. Confined to this
// _test.go file per the ticket contract; the REAL, non-stub implementation
// lives in plugins/examples/example-builtin/plugin.go.
type fakeHandlers struct{}

func (fakeHandlers) DispatchTool(context.Context, string, []byte) ([]byte, error) { return nil, nil }
func (fakeHandlers) DispatchIntent(context.Context, string, []byte) ([]byte, error) {
	return nil, nil
}
func (fakeHandlers) RunCommand(context.Context, string, []string) error { return nil }

func testManifest(id string) Manifest {
	return Manifest{
		ID:          id,
		Name:        "Test " + id,
		Schema:      SchemaVersion,
		Version:     "0.1.0",
		HostVersion: ">=0.1.0",
		Runtime:     RuntimeBuiltin,
	}
}

func TestBuiltinRegistry_RegisterSeedsReadGrant(t *testing.T) {
	var r builtinRegistry
	r.register(testManifest("a"), fakeHandlers{})

	snap := r.snapshot()
	if len(snap) != 1 {
		t.Fatalf("snapshot: got %d entries, want 1", len(snap))
	}
	if got := snap[0].Grants; len(got) != 1 || got[0] != "read" {
		t.Fatalf("Grants = %v, want [read]", got)
	}
}

func TestBuiltinRegistry_SnapshotSortedByID(t *testing.T) {
	var r builtinRegistry
	// Register out of id order on purpose.
	r.register(testManifest("zebra"), fakeHandlers{})
	r.register(testManifest("apple"), fakeHandlers{})
	r.register(testManifest("mango"), fakeHandlers{})

	snap := r.snapshot()
	if len(snap) != 3 {
		t.Fatalf("snapshot: got %d entries, want 3", len(snap))
	}
	want := []string{"apple", "mango", "zebra"}
	for i, id := range want {
		if snap[i].Manifest.ID != id {
			t.Errorf("snapshot[%d].Manifest.ID = %q, want %q", i, snap[i].Manifest.ID, id)
		}
	}
}

func TestBuiltinRegistry_SnapshotFreezesRegistry(t *testing.T) {
	var r builtinRegistry
	r.register(testManifest("first"), fakeHandlers{})

	first := r.snapshot()
	if len(first) != 1 {
		t.Fatalf("first snapshot: got %d entries, want 1", len(first))
	}

	// A registration after the first snapshot() call is a documented
	// no-op: it must not appear in any later snapshot() call, and
	// snapshot() must keep returning the SAME frozen slice, never
	// recomputed.
	r.register(testManifest("late"), fakeHandlers{})

	second := r.snapshot()
	if len(second) != 1 {
		t.Fatalf("second snapshot: got %d entries, want 1 (late registration must be dropped)", len(second))
	}
	if second[0].Manifest.ID != "first" {
		t.Fatalf("second snapshot[0].Manifest.ID = %q, want %q", second[0].Manifest.ID, "first")
	}
}

func TestBuiltinRegistry_ConcurrentRegisterIsRace(t *testing.T) {
	var r builtinRegistry
	const n = 32

	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			r.register(testManifest(fmt.Sprintf("plugin-%02d", i)), fakeHandlers{})
		}(i)
	}
	wg.Wait()

	snap := r.snapshot()
	if len(snap) != n {
		t.Fatalf("snapshot: got %d entries, want %d", len(snap), n)
	}
	for i := 1; i < len(snap); i++ {
		if snap[i-1].Manifest.ID >= snap[i].Manifest.ID {
			t.Fatalf("snapshot not sorted at index %d: %q >= %q", i, snap[i-1].Manifest.ID, snap[i].Manifest.ID)
		}
	}
}

func TestBuiltinRegistry_ConcurrentSnapshotReturnsSameSlice(t *testing.T) {
	var r builtinRegistry
	r.register(testManifest("only"), fakeHandlers{})

	const n = 16
	results := make([][]BuiltinRegistration, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			results[i] = r.snapshot()
		}(i)
	}
	wg.Wait()

	for i := 1; i < n; i++ {
		if len(results[i]) != len(results[0]) || (len(results[i]) > 0 && results[i][0].Manifest.ID != results[0][0].Manifest.ID) {
			t.Fatalf("concurrent snapshot() calls disagreed: %v vs %v", results[0], results[i])
		}
	}
}

// ExampleRegisterBuiltin is this package's runnable Art.10.6 Example for
// the registration entry point. It is the ONE place in this package's
// tests that touches the process-global defaultBuiltinRegistry (via the
// exported RegisterBuiltin/Builtins functions) — every other test above
// uses a private builtinRegistry instance instead, precisely so it does
// not collide with this Example's use of (and permanent freeze of) the
// real global.
func ExampleRegisterBuiltin() {
	RegisterBuiltin(Manifest{
		ID:          "example-registration",
		Name:        "Example Registration",
		Schema:      SchemaVersion,
		Version:     "0.1.0",
		HostVersion: ">=0.1.0",
		Runtime:     RuntimeBuiltin,
	}, fakeHandlers{})

	for _, reg := range Builtins() {
		if reg.Manifest.ID == "example-registration" {
			fmt.Println(reg.Manifest.ID, reg.Grants)
		}
	}
	// Output: example-registration [read]
}
