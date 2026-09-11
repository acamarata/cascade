package census

import (
	"sync"
	"testing"
)

// TestNewCollector_ZeroState proves NewCollector's zero state is
// (nil, false) from Latest and 0 from Count, before any Store call.
func TestNewCollector_ZeroState(t *testing.T) {
	c := NewCollector()
	got, ok := c.Latest()
	if got != nil || ok {
		t.Fatalf("Latest() = (%v, %v), want (nil, false)", got, ok)
	}
	if n := c.Count(); n != 0 {
		t.Fatalf("Count() = %d, want 0", n)
	}
}

// TestCollector_StoreLatestRoundTrip proves Store followed by Latest
// returns the same entries and true, and Count matches len(Latest).
func TestCollector_StoreLatestRoundTrip(t *testing.T) {
	c := NewCollector()
	want := []Snapshot{
		{Pid: 1, Binary: "claude", Account: "a1"},
		{Pid: 2, Binary: "codex", Account: "a2"},
	}
	c.Store(want)

	got, ok := c.Latest()
	if !ok {
		t.Fatalf("Latest() ok = false, want true")
	}
	if len(got) != len(want) {
		t.Fatalf("Latest() len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Pid != want[i].Pid || got[i].Binary != want[i].Binary || got[i].Account != want[i].Account {
			t.Fatalf("Latest()[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
	if n := c.Count(); n != len(want) {
		t.Fatalf("Count() = %d, want %d", n, len(want))
	}
}

// TestCollector_LatestReturnsCopy proves the caller cannot mutate the
// Collector's stored state through the slice Latest returns.
func TestCollector_LatestReturnsCopy(t *testing.T) {
	c := NewCollector()
	c.Store([]Snapshot{{Pid: 1, Binary: "claude"}})

	got, ok := c.Latest()
	if !ok {
		t.Fatalf("Latest() ok = false, want true")
	}
	got[0].Binary = "mutated"

	again, ok := c.Latest()
	if !ok {
		t.Fatalf("second Latest() ok = false, want true")
	}
	if again[0].Binary != "claude" {
		t.Fatalf("stored state mutated via Latest()'s returned slice: got %q, want %q", again[0].Binary, "claude")
	}
}

// TestCollector_StoreNilResets proves Store(nil) resets to (nil, false) / 0.
func TestCollector_StoreNilResets(t *testing.T) {
	c := NewCollector()
	c.Store([]Snapshot{{Pid: 1, Binary: "claude"}})
	c.Store(nil)

	got, ok := c.Latest()
	if got != nil || ok {
		t.Fatalf("Latest() after Store(nil) = (%v, %v), want (nil, false)", got, ok)
	}
	if n := c.Count(); n != 0 {
		t.Fatalf("Count() after Store(nil) = %d, want 0", n)
	}
}

// TestCollector_StoreEmptyResets proves Store([]Snapshot{}) resets to
// (nil, false) / 0, same as Store(nil).
func TestCollector_StoreEmptyResets(t *testing.T) {
	c := NewCollector()
	c.Store([]Snapshot{{Pid: 1, Binary: "claude"}})
	c.Store([]Snapshot{})

	got, ok := c.Latest()
	if got != nil || ok {
		t.Fatalf("Latest() after Store(empty) = (%v, %v), want (nil, false)", got, ok)
	}
	if n := c.Count(); n != 0 {
		t.Fatalf("Count() after Store(empty) = %d, want 0", n)
	}
}

// TestCollector_StoreIdempotentOnEqualSlices proves repeated Store calls
// with equal slices produce a consistent result, with no panic.
func TestCollector_StoreIdempotentOnEqualSlices(t *testing.T) {
	c := NewCollector()
	snap := []Snapshot{{Pid: 7, Binary: "claude", Account: "a1"}}
	for i := 0; i < 3; i++ {
		c.Store(snap)
	}
	got, ok := c.Latest()
	if !ok || len(got) != 1 || got[0].Pid != snap[0].Pid || got[0].Binary != snap[0].Binary {
		t.Fatalf("Latest() after repeated Store = (%+v, %v), want ([%+v], true)", got, ok, snap[0])
	}
	if n := c.Count(); n != 1 {
		t.Fatalf("Count() = %d, want 1", n)
	}
}

// TestCollector_ConcurrentAccess proves Latest, Count and Store are safe
// under concurrent use (run with -race).
func TestCollector_ConcurrentAccess(t *testing.T) {
	t.Helper()
	c := NewCollector()
	var wg sync.WaitGroup
	const workers = 8
	for i := 0; i < workers; i++ {
		wg.Add(3)
		go func(n int) {
			defer wg.Done()
			c.Store([]Snapshot{{Pid: n, Binary: "claude"}})
		}(i)
		go func() {
			defer wg.Done()
			_, _ = c.Latest()
		}()
		go func() {
			defer wg.Done()
			_ = c.Count()
		}()
	}
	wg.Wait()
}

// TestCollector_ImplementsReader is a compile-check that Collector
// satisfies Reader through a variable of the interface type,
// independent of reader.go's own package-level assertion.
func TestCollector_ImplementsReader(t *testing.T) {
	var r Reader = NewCollector()
	if _, ok := r.Latest(); ok {
		t.Fatalf("fresh Collector via Reader Latest() ok = true, want false")
	}
}
