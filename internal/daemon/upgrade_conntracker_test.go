//go:build !windows

package daemon

// Purpose: Drain and ConnTracker tests (upgrade.go, upgrade_conntracker.go),
// plus the restart-path and non-bricking contract tests that need a
// listener stand-in. Split from upgrade_test.go purely for the 300-line
// file cap. No "net" import (Art.7.2): fakeListener below satisfies the
// io.Closer Drain/AttemptUpgrade actually need; the real refuse-after-close
// proof against a genuine socket lives in the `integration`-tagged sibling.
// SPORT: internal/daemon (ADD, per T-5 sport_updates).

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
)

// mockConn is an in-flight "handler" for the drain tests: Close marks
// itself closed so CloseAll's force-close is directly observable.
type mockConn struct {
	closed atomic.Bool
}

func (c *mockConn) Close() error { c.closed.Store(true); return nil }

// fakeListener stands in for a real net.Listener: Drain and AttemptUpgrade
// only need io.Closer, so a fake with nothing but Close() proves the same
// "stopped accepting" behavior without a real socket or a "net" import.
type fakeListener struct {
	closed atomic.Bool
}

func (f *fakeListener) Close() error { f.closed.Store(true); return nil }

// TestDrainGrace: an in-flight handler that finishes within grace lets
// Drain return promptly without force-closing it. The handler's
// completion is driven deterministically from the injected Sleep callback
// (Drain's own poll tick) rather than a second goroutine racing Drain's
// loop against real OS scheduling — the same "advance the clock, never
// sleep for real" discipline every other test in this package uses,
// applied here to avoid a flaky race between an unsynchronized goroutine
// and a busy-poll loop over a fake clock.
func TestDrainGrace(t *testing.T) {
	m, _ := newTestManager(t, nil, nil)
	ln := &fakeListener{}
	tracker := NewConnTracker()
	conn := &mockConn{}
	tracker.Begin(conn)

	firstTick := true
	m.Sleep = func(d time.Duration) {
		m.Clock.(*runtime.FixedClock).Advance(d)
		if firstTick {
			firstTick = false
			tracker.End(conn) // completes well within the 5s grace window
		}
	}

	if err := m.Drain(context.Background(), ln, tracker, 5*time.Second); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if conn.closed.Load() {
		t.Fatal("Drain force-closed a connection that completed within grace")
	}
	if !m.Draining() {
		t.Fatal("Draining() = false after Drain")
	}
	if !ln.closed.Load() {
		t.Fatal("Drain: want the listener closed (stop accepting new connections)")
	}
}

// TestDrainTimeout: a handler that never finishes is force-closed once
// grace elapses, and Drain returns without blocking indefinitely.
func TestDrainTimeout(t *testing.T) {
	m, _ := newTestManager(t, nil, nil)
	ln := &fakeListener{}
	tracker := NewConnTracker()
	conn := &mockConn{}
	tracker.Begin(conn) // never End()'d: models a stuck handler

	done := make(chan error, 1)
	go func() { done <- m.Drain(context.Background(), ln, tracker, 30*time.Millisecond) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Drain: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Drain blocked indefinitely past its grace window")
	}
	if !conn.closed.Load() {
		t.Fatal("Drain: want the stuck connection force-closed at grace boundary")
	}
}

// TestRestartTriggersUpgrade is the restart-path contract test: a skewed
// on-disk binary drives Drain + Relaunch; matching hashes perform a
// logged no-op and never touch execFunc.
func TestRestartTriggersUpgrade(t *testing.T) {
	// Stamp a digest so this exercises the real skew comparison. An
	// unstamped build now reports no skew by design, and without this
	// the test would pass only because the sentinel never equals a hash,
	// which is the bug that made dev builds relaunch on every shutdown.
	setBuildHash(t, "1111111111111111111111111111111111111111111111111111111111111111")
	t.Run("skew", func(t *testing.T) {
		orig := execFunc
		t.Cleanup(func() { execFunc = orig })
		var execCalled bool
		execFunc = func(string, []string, []string) error { execCalled = true; return nil }

		m, _ := newTestManager(t, nil, nil)
		ln := &fakeListener{}
		path := writeTempBinary(t, "definitely-not-buildhash")

		relaunched, err := m.AttemptUpgrade(context.Background(), path, ln, nil, time.Second, []string{path}, nil)
		if err != nil {
			t.Fatalf("AttemptUpgrade: %v", err)
		}
		if !relaunched || !execCalled {
			t.Fatalf("AttemptUpgrade(skew) relaunched=%v execCalled=%v; want both true", relaunched, execCalled)
		}
		if !m.Draining() || !ln.closed.Load() {
			t.Fatal("AttemptUpgrade(skew): want Drain to have run and closed the listener")
		}
	})

	t.Run("no-skew", func(t *testing.T) {
		origHash := buildHash
		orig := execFunc
		t.Cleanup(func() { buildHash = origHash; execFunc = orig })
		path := writeTempBinary(t, "same-bits")
		sum, err := hashFile(path)
		if err != nil {
			t.Fatalf("hashFile: %v", err)
		}
		buildHash = sum
		var execCalled bool
		execFunc = func(string, []string, []string) error { execCalled = true; return nil }

		m, _ := newTestManager(t, nil, nil)
		relaunched, err := m.AttemptUpgrade(context.Background(), path, nil, nil, time.Second, nil, nil)
		if err != nil || relaunched || execCalled {
			t.Fatalf("AttemptUpgrade(no-skew) = %v, %v, execCalled=%v; want false, nil, false", relaunched, err, execCalled)
		}
	})
}

// TestAttemptUpgrade_RelaunchFailure_NonBricking proves this ticket's core
// failure-path requirement: a skewed binary that fails to exec leaves
// AttemptUpgrade reporting relaunched=false with a typed error, having
// already drained (so a caller falling through to a normal exit — as
// lifecycle_unix.go's attemptUpgrade does — converges on a clean,
// recoverable "not running" state rather than a half-alive one).
func TestAttemptUpgrade_RelaunchFailure_NonBricking(t *testing.T) {
	// Stamp a digest so this exercises the real skew comparison. An
	// unstamped build now reports no skew by design, and without this
	// the test would pass only because the sentinel never equals a hash,
	// which is the bug that made dev builds relaunch on every shutdown.
	setBuildHash(t, "1111111111111111111111111111111111111111111111111111111111111111")
	orig := execFunc
	t.Cleanup(func() { execFunc = orig })
	execFunc = func(string, []string, []string) error { return errors.New("exec format error") }

	m, _ := newTestManager(t, nil, nil)
	ln := &fakeListener{}
	path := writeTempBinary(t, "skewed-binary")

	relaunched, err := m.AttemptUpgrade(context.Background(), path, ln, nil, time.Second, []string{path}, nil)
	if relaunched {
		t.Fatal("AttemptUpgrade: want relaunched=false on exec failure")
	}
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("AttemptUpgrade error = %v; want KindUnavailable", err)
	}
	if !ln.closed.Load() {
		t.Fatal("listener not closed after a failed relaunch attempt (fall-through must not brick)")
	}
}

// TestShutdownRequestedEvent proves Drain publishes
// EventKindShutdownRequested before it waits for in-flight work.
func TestShutdownRequestedEvent(t *testing.T) {
	store := storetest.NewMemStore()
	clk := runtime.NewFixedClock(time.Now())
	bus := events.New(store, clk)
	m, _ := newTestManager(t, store, bus)
	m.Clock = clk
	m.Sleep = advancingSleep(clk)

	if err := m.Drain(context.Background(), &fakeListener{}, nil, time.Second); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	evs, err := bus.Replay(context.Background(), eventNamespace, 0)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if len(evs) != 1 || evs[0].Kind != EventKindShutdownRequested {
		t.Fatalf("events = %+v; want exactly one ShutdownRequested", evs)
	}
}

// TestConnTracker_BeginEndDone proves the WaitGroup half in isolation
// (no Drain involved): Done() closes only once every Begin has a
// matching End.
func TestConnTracker_BeginEndDone(t *testing.T) {
	tr := NewConnTracker()
	var wg sync.WaitGroup
	tr.Begin(nil)
	tr.Begin(nil)
	wg.Add(1)
	go func() { defer wg.Done(); tr.End(nil); tr.End(nil) }()
	wg.Wait()

	select {
	case <-tr.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("Done() never closed after matching Begin/End pairs")
	}
}

// TestConnTracker_CloseAllReportsCount proves CloseAll force-closes every
// still-registered connection and reports how many.
func TestConnTracker_CloseAllReportsCount(t *testing.T) {
	tr := NewConnTracker()
	a, b := &mockConn{}, &mockConn{}
	tr.Begin(a)
	tr.Begin(b)
	if n := tr.CloseAll(); n != 2 {
		t.Fatalf("CloseAll = %d, want 2", n)
	}
	if !a.closed.Load() || !b.closed.Load() {
		t.Fatal("CloseAll did not close every tracked connection")
	}
	if n := tr.CloseAll(); n != 0 {
		t.Fatalf("second CloseAll = %d, want 0 (already empty)", n)
	}
}
