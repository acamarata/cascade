//go:build !windows

// Purpose: unit tests for daemon_unix_memory_projection.go's loop
//
//	(runMemoryProjectionLoop/runMemoryProjectionOnce) -- driven by a fake
//	memoryProjectionRunner and a manually-ticked fake runtime.Ticker
//	(mirroring internal/fleet/headroom_test.go's fakeTicker), synchronized
//	through the afterRun hook the same way
//	internal/fleet.HeadroomPublisher's own afterTick field is (never a
//	sleep, never a bare polling loop -- R-14.136). startMemoryProjection's
//	own composition (a real *memory.ProjectionJob over the daemon's
//	store) is exercised by the daemon's own start path; this file proves
//	the LOOP's behaviour in isolation.
//
// SPORT: cmd/cascade daemon_unix_memory_projection.go [ADD] (P1-E07-W5-S92-T1).
package main

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/memory"
	"github.com/acamarata/cascade/pkg/cascade"
)

// fakeProjectionRunner is a manually-driven memoryProjectionRunner: each
// call to Run records itself and pops the next canned (result, error)
// pair off a queue, repeating the last entry once the queue is drained.
type fakeProjectionRunner struct {
	mu    sync.Mutex
	calls int
	queue []fakeRunOutcome
}

type fakeRunOutcome struct {
	res memory.ProjectionResult
	err error
}

func (f *fakeProjectionRunner) Run(context.Context) (memory.ProjectionResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if len(f.queue) == 0 {
		return memory.ProjectionResult{}, nil
	}
	out := f.queue[0]
	if len(f.queue) > 1 {
		f.queue = f.queue[1:]
	}
	return out.res, out.err
}

func (f *fakeProjectionRunner) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// memProjFakeTicker is a manually-driven runtime.Ticker, mirroring
// internal/fleet/headroom_test.go's fakeTicker: Tick fires exactly one
// tick, blocking until the loop's select accepts it or ctx is done.
type memProjFakeTicker struct {
	c        chan struct{}
	stopOnce sync.Once
	stopped  chan struct{}
}

func newMemProjFakeTicker() *memProjFakeTicker {
	return &memProjFakeTicker{c: make(chan struct{}), stopped: make(chan struct{})}
}

func (f *memProjFakeTicker) C() <-chan struct{} { return f.c }
func (f *memProjFakeTicker) Stop()              { f.stopOnce.Do(func() { close(f.stopped) }) }
func (f *memProjFakeTicker) Tick(ctx context.Context) {
	select {
	case f.c <- struct{}{}:
	case <-ctx.Done():
	}
}

// discardMemProjLogger writes to io.Discard -- these tests assert on Run
// call counts and the afterRun hook, not log text.
func discardMemProjLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// awaitPass blocks on settled for one afterRun firing, or fails the test
// after a bounded timeout -- never a real 60-second wait for the
// production tick interval (R-14.136).
func awaitPass(t *testing.T, settled <-chan struct{}) {
	t.Helper()
	select {
	case <-settled:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a memory projection pass to settle")
	}
}

// tick delivers one tick under its own bounded timeout, mirroring
// internal/fleet/headroom_publish_test.go's tickCtx pattern: a loop that
// has already stopped listening (returned, or never started) must fail
// this call's deadline rather than hang the whole test binary forever,
// the same defensive shape awaitPass's own timeout gives the settle side.
func tick(t *testing.T, ft *memProjFakeTicker) {
	t.Helper()
	tickCtx, tickCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer tickCancel()
	ft.Tick(tickCtx)
	if tickCtx.Err() != nil {
		t.Fatal("timed out delivering a tick -- the loop is not listening on Ticker.C()")
	}
}

// TestMemoryProjectionLoop_RunsAtStartAndOnTick proves the immediate start
// run plus one run per tick (R-14.294: "interval 60s plus a start run").
func TestMemoryProjectionLoop_RunsAtStartAndOnTick(t *testing.T) {
	job := &fakeProjectionRunner{}
	ticker := newMemProjFakeTicker()
	settled := make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		runMemoryProjectionLoop(ctx, job, ticker, discardMemProjLogger(), func() { settled <- struct{}{} })
		close(done)
	}()

	awaitPass(t, settled) // the immediate start run
	if got := job.callCount(); got != 1 {
		t.Fatalf("Run call count after start = %d, want 1", got)
	}

	tick(t, ticker)
	awaitPass(t, settled)
	if got := job.callCount(); got != 2 {
		t.Fatalf("Run call count after one tick = %d, want 2", got)
	}

	tick(t, ticker)
	awaitPass(t, settled)
	if got := job.callCount(); got != 3 {
		t.Fatalf("Run call count after two ticks = %d, want 3", got)
	}

	cancel()
	<-done
}

// TestMemoryProjectionLoop_ErrorLoggedAndRetried proves a failing run is
// swallowed (never panics the loop, never returns) and the next tick
// tries again.
func TestMemoryProjectionLoop_ErrorLoggedAndRetried(t *testing.T) {
	job := &fakeProjectionRunner{queue: []fakeRunOutcome{
		{err: cascade.New(cascade.KindUnavailable, "disk gone")},
		{res: memory.ProjectionResult{Scanned: 1, Upserted: 1}},
	}}
	ticker := newMemProjFakeTicker()
	settled := make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		runMemoryProjectionLoop(ctx, job, ticker, discardMemProjLogger(), func() { settled <- struct{}{} })
		close(done)
	}()

	awaitPass(t, settled) // the failing start run
	if got := job.callCount(); got != 1 {
		t.Fatalf("Run call count after the failing start run = %d, want 1", got)
	}

	tick(t, ticker)
	awaitPass(t, settled) // retried, not skipped, not a crash
	if got := job.callCount(); got != 2 {
		t.Fatalf("Run call count after retry = %d, want 2 (the loop must retry on the next tick)", got)
	}

	cancel()
	<-done
}

// TestMemoryProjectionLoop_StopsWithContext proves the loop returns (and
// stops the ticker) when ctx ends, and never fires another pass after.
func TestMemoryProjectionLoop_StopsWithContext(t *testing.T) {
	job := &fakeProjectionRunner{}
	ticker := newMemProjFakeTicker()
	settled := make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		runMemoryProjectionLoop(ctx, job, ticker, discardMemProjLogger(), func() { settled <- struct{}{} })
		close(done)
	}()

	awaitPass(t, settled) // the start run, proving the loop is live
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("runMemoryProjectionLoop did not return after ctx was cancelled")
	}
	select {
	case <-ticker.stopped:
	default:
		t.Fatal("runMemoryProjectionLoop did not stop the ticker on exit")
	}
}
