package daemon

// Purpose: the daemon's one goroutine supervisor. Every long-lived
//
//	background goroutine the daemon starts goes through GoSupervised (or,
//	for the Register* methods that predate it, goSubsystem), so Wait joins
//	it before the store closes. Split out of subsystems.go to stay under
//	Art.10.3's 300-line cap.
//
// Inputs: the run context and the function a subsystem wants to run.
// Outputs: a goroutine the Manifest can wait for, and its final state in the
//
//	manifest: Failed on an error or a recovered panic, Skipped("stopped") on
//	a clean return.
//
// Constraints: Wait joins, it does not cancel. A caller cancels the
//
//	subsystems' context first and then waits; Wait on a live context blocks
//	for as long as the subsystem runs, which is forever for a healthy
//	daemon. A supervised panic never propagates: it is recovered, recorded
//	and logged once with its stack through the daemon logger.
//
// SPORT: internal/daemon (CHANGED, supervised goroutines and run-context join).

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"time"
)

// supervisedStoppedDetail is the manifest detail of a supervised goroutine
// that returned nil: a clean stop, reported as Skipped so status.get keeps
// its existing state set.
const supervisedStoppedDetail = "stopped"

// goSubsystem runs fn in a tracked goroutine.
//
// Every long-lived subsystem goroutine goes through here so that Wait can
// join it. Firing them off untracked was a real defect and not only an
// untidiness: cancelling a context returns immediately while the goroutine
// is still mid-operation, so a caller that then removes the subsystem's
// working directory races it. On Unix that is invisible, because unlink
// succeeds on an open file; on Windows the open handle makes the removal
// fail outright, which is how this surfaced (a CI-only
// "The directory is not empty" on a test's own TempDir cleanup).
func (m *Manifest) goSubsystem(fn func()) {
	m.running.Add(1)
	go func() {
		defer m.running.Done()
		fn()
	}()
}

// GoSupervised runs fn(ctx) as the tracked subsystem name and records its
// outcome. It registers name and marks it Started with detail before the
// goroutine starts, so a fast return can never be overwritten by a late
// Started. When fn returns, name becomes Failed (an error), Failed with a
// detail starting "panic:" (a recovered panic, logged once with its stack),
// or Skipped with supervisedStoppedDetail (a nil return, which is what a
// cancelled run context produces).
//
// It is the one way daemon code starts a background goroutine: Wait joins
// it, and a panic in one consumer cannot take the daemon down.
func (m *Manifest) GoSupervised(ctx context.Context, name, detail string, fn func(context.Context) error) {
	m.Register(name)
	m.Started(name, detail)
	m.goSubsystem(func() { m.runSupervised(ctx, name, fn) })
}

// runSupervised is GoSupervised's goroutine body: it runs fn, recovers a
// panic and records the final state.
func (m *Manifest) runSupervised(ctx context.Context, name string, fn func(context.Context) error) {
	defer func() {
		if v := recover(); v != nil {
			m.recordPanic(name, v, debug.Stack())
		}
	}()
	if err := fn(ctx); err != nil {
		m.set(name, SubsystemError, err.Error())
		m.logf(slog.LevelError, name, "subsystem failed", err.Error())
		return
	}
	m.set(name, SubsystemSkipped, supervisedStoppedDetail)
	m.logf(slog.LevelInfo, name, "subsystem stopped", supervisedStoppedDetail)
}

// recordPanic marks name failed with a "panic:" detail and writes the one
// ERROR line that carries the stack. The stack goes to the daemon logger
// only, never to stdout or the manifest detail status.get serves.
func (m *Manifest) recordPanic(name string, v any, stack []byte) {
	detail := fmt.Sprintf("panic: %v", v)
	m.set(name, SubsystemError, detail)
	if m.log == nil {
		return
	}
	m.log.Error("subsystem panicked", slog.String("subsystem", name),
		slog.String("detail", detail), slog.String("stack", string(stack)))
}

// Wait blocks until every subsystem goroutine this Manifest started has
// returned. Cancel their context first — Wait does not cancel anything, it
// only joins.
func (m *Manifest) Wait() { m.running.Wait() }

// waitContext is Wait bounded by ctx. It reports true once every supervised
// goroutine has returned and false when ctx ends first. On false the waiter
// keeps waiting and exits with the last supervised goroutine, so it never
// outlives what it waits for. Only RelaunchJoin uses it: a relaunch must not
// block forever, while a shutdown waits unbounded.
func (m *Manifest) waitContext(ctx context.Context) bool {
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.running.Wait()
	}()
	select {
	case <-done:
		return true
	case <-ctx.Done():
		return false
	}
}

// RelaunchJoin returns the UpgradeManager.BeforeRelaunch hook for a daemon
// whose supervised goroutines run under the context cancelRun ends: the hook
// cancels that run context and joins every supervised goroutine, bounded by
// grace. A join that outlives grace is logged and the relaunch goes ahead
// anyway, because a stuck goroutine must never block an upgrade.
func (m *Manifest) RelaunchJoin(cancelRun context.CancelFunc, grace time.Duration) func(context.Context) {
	return func(ctx context.Context) {
		cancelRun()
		bound, cancel := context.WithTimeout(context.WithoutCancel(ctx), grace)
		defer cancel()
		if !m.waitContext(bound) {
			m.logf(slog.LevelWarn, relaunchJoinName, "supervised goroutines outlived the drain grace; relaunching anyway", grace.String())
		}
	}
}

// relaunchJoinName is the subsystem field RelaunchJoin's timeout line
// carries. It is a log label only, never a manifest row.
const relaunchJoinName = "upgrade.relaunch-join"

// harnessSessionWatchSubsystem is the manifest name for the harness
// session watch.
const harnessSessionWatchSubsystem = "harness-session-watch"

// RegisterHarnessSessionWatch runs a harness plugin's session watch as a
// supervised daemon subsystem.
//
// The watch is passed as a bare func rather than a typed collaborator on
// purpose: internal/daemon must not import internal/plugins to start
// something internal/plugins composed, or the daemon's subsystem registry
// would depend on the plugin catalog it exists to supervise.
//
// A watch that returns an error is recorded as Failed. A watch that returns
// nil has stopped cleanly, which is what a cancelled context produces, and
// is recorded as Skipped("stopped"), not a failure.
func (m *Manifest) RegisterHarnessSessionWatch(ctx context.Context, run func(context.Context) error) {
	m.Register(harnessSessionWatchSubsystem)
	if run == nil {
		m.Skipped(harnessSessionWatchSubsystem, "no harness watch wired")
		return
	}
	m.GoSupervised(ctx, harnessSessionWatchSubsystem, "watching harness sessions", run)
}
