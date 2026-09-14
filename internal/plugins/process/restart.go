// Purpose: crash isolation for a launched plugin process: a health
//
//	monitor goroutine watches the process exit, restarts it with
//	exponential backoff up to a configured attempt budget, and on
//	exhaustion transitions the handle to state-invalid so every
//	subsequent call fails fast with ErrPluginUnavailable.
//
// Inputs: a Waiter (the running process's exit), a RestartPolicy, an
//
//	AuditSink, and the injected Clock for backoff scheduling.
//
// Outputs: state transitions on a *stateBox; a CrashReport per exit,
//
//	delivered to the AuditSink.
//
// Constraints: no bare time.Sleep for backoff — the monitor computes the
//
//	delay and waits on a timer built from context, never a raw
//	time.Sleep call. State reads/writes are mutex-guarded so Handle's
//	call path and the monitor goroutine never race.
//
// SPORT: internal/plugins/process restart (ADD) — P1-E15-W4-S31-T3.

package process

import (
	"context"
	"sync"
	"time"
)

// RestartPolicy bounds how a crashed plugin process is restarted.
type RestartPolicy struct {
	// MaxAttempts is the number of restarts allowed after the first
	// launch before the plugin is marked state-invalid. Zero means no
	// restarts: one crash is terminal.
	MaxAttempts int
	// InitialBackoff is the delay before the first restart. Each
	// subsequent attempt doubles the previous delay.
	InitialBackoff time.Duration
}

// DefaultRestartPolicy is used when a ProcessRuntime is built with a zero
// RestartPolicy.
var DefaultRestartPolicy = RestartPolicy{MaxAttempts: 3, InitialBackoff: time.Second}

// backoffFor returns the delay before restart attempt n (1-indexed):
// InitialBackoff * 2^(n-1).
func (p RestartPolicy) backoffFor(n int) time.Duration {
	if p.InitialBackoff <= 0 {
		p.InitialBackoff = time.Second
	}
	d := p.InitialBackoff
	for i := 1; i < n; i++ {
		d *= 2
	}
	return d
}

// resolved returns p with zero fields filled from DefaultRestartPolicy.
func (p RestartPolicy) resolved() RestartPolicy {
	if p.MaxAttempts == 0 && p.InitialBackoff == 0 {
		return DefaultRestartPolicy
	}
	if p.InitialBackoff <= 0 {
		p.InitialBackoff = DefaultRestartPolicy.InitialBackoff
	}
	return p
}

// pluginState is a Handle's lifecycle state.
type pluginState int

const (
	stateRunning pluginState = iota
	stateInvalid
)

// stateBox holds a Handle's mutable lifecycle state under a mutex.
type stateBox struct {
	mu           sync.RWMutex
	state        pluginState
	restartCount int
}

// get reads the current state.
func (s *stateBox) get() pluginState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state
}

// markInvalid transitions to state-invalid. Idempotent.
func (s *stateBox) markInvalid() {
	s.mu.Lock()
	s.state = stateInvalid
	s.mu.Unlock()
}

// incrementRestart records one more restart attempt and returns the new
// count.
func (s *stateBox) incrementRestart() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.restartCount++
	return s.restartCount
}

// CrashReport describes one plugin process exit, delivered to the
// AuditSink.
type CrashReport struct {
	// PluginName identifies the plugin.
	PluginName string
	// ExitCode is the process's exit status, or -1 when it could not be
	// determined (e.g. killed by signal).
	ExitCode int
	// StderrTail is the last captured stderr lines, at most 64.
	StderrTail []string
	// RestartCount is how many restarts have been attempted so far,
	// including this one.
	RestartCount int
	// Final reports whether this crash exhausted the restart budget.
	Final bool
}

// AuditSink receives crash reports. A nil AuditSink is valid: LogCrash is
// simply not called.
type AuditSink interface {
	LogCrash(report CrashReport)
}

// Waiter is the seam onto a running process's exit, satisfied by
// *exec.Cmd. Decoupling from os/exec directly lets the monitor be driven
// by a fake in tests without forking a real process.
type Waiter interface {
	Wait() error
}

// exitCoder is implemented by the *exec.ExitError Wait returns on a
// non-zero exit.
type exitCoder interface {
	ExitCode() int
}

// waitExitCode blocks on w.Wait and reports the process's exit code. -1
// means the code could not be determined.
func waitExitCode(w Waiter) int {
	err := w.Wait()
	if err == nil {
		return 0
	}
	if ec, ok := err.(exitCoder); ok {
		return ec.ExitCode()
	}
	return -1
}

// stderrTailer captures the last N lines a process wrote to stderr, for
// inclusion in a CrashReport. Safe for concurrent Write and Lines calls.
type stderrTailer struct {
	mu    sync.Mutex
	lines []string
	max   int
}

// newStderrTailer returns a tailer keeping at most maxLines lines.
func newStderrTailer(maxLines int) *stderrTailer {
	return &stderrTailer{max: maxLines}
}

// add appends one line, evicting the oldest when over capacity.
func (t *stderrTailer) add(line string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lines = append(t.lines, line)
	if len(t.lines) > t.max {
		t.lines = t.lines[len(t.lines)-t.max:]
	}
}

// snapshot returns a copy of the captured lines.
func (t *stderrTailer) snapshot() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]string, len(t.lines))
	copy(out, t.lines)
	return out
}

// backoffSleep blocks for d or until ctx is done, whichever comes first.
// It is the monitor's only wait primitive, deliberately not a bare
// time.Sleep, so a restart loop under test can be canceled promptly via
// ctx rather than sitting through the full delay.
func backoffSleep(ctx context.Context, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
	}
}

// Handle is a running plugin process: its manifest, the handshake ack it
// negotiated, and the transport calls go through. A Handle is safe for
// concurrent use; the crash monitor swaps its transport out from under a
// caller on restart, guarded by mu.
type Handle struct {
	// Manifest is the manifest Launch was called with.
	Manifest Manifest
	// Ack is the handshake response the plugin returned on (re)launch.
	Ack HelloAckMsg

	mu        sync.RWMutex
	transport *Transport
	state     *stateBox
	tail      *stderrTailer
	// lifetimeCtx is the context governing how long this plugin's
	// process may run. It is stored rather than passed because a
	// respawn happens on the monitor goroutine, long after the Launch
	// call that owns the lifetime has returned — and it must never be
	// the handshake's bounded context (lifetime.go's Constraints).
	lifetimeCtx context.Context
}

// DrainGrace bounds how long awaitReadsDone waits for the plugin's stdout
// to reach EOF.
//
// It is a liveness backstop, never evidence of anything. For an ordinary
// plugin the process's exit closes its stdout, so EOF arrives within
// microseconds and this bound is never approached. It exists for the case
// that genuinely cannot reach EOF: a plugin that forked a grandchild which
// inherited stdout and outlived it. There the write end stays open after
// the plugin is gone, and an unbounded wait would hang the crash monitor
// forever -- trading a lost final frame for a supervisor that never
// notices the crash at all, which is strictly worse.
var DrainGrace = 5 * time.Second

// awaitReadsDone blocks until every byte the plugin wrote to stdout has
// been read, so a caller may then Wait on the process without discarding
// the plugin's final frames (Transport.Done explains the os/exec contract
// this exists to honour). A Handle with no transport has nothing to drain.
func (h *Handle) awaitReadsDone() {
	h.mu.RLock()
	t := h.transport
	h.mu.RUnlock()
	if t == nil {
		return
	}
	timer := time.NewTimer(DrainGrace)
	defer timer.Stop()
	select {
	case <-t.Done():
	case <-timer.C:
	}
}

// lifetime returns h's process-lifetime context, defaulting to
// context.Background for a Handle built without one (every test
// constructing a Handle literal): a zero lifetime must mean "not
// bounded", never a nil context that panics on the next spawn.
func (h *Handle) lifetime() context.Context {
	if h.lifetimeCtx == nil {
		return context.Background()
	}
	return h.lifetimeCtx
}

// Call performs one JSON-RPC round trip through the current transport. A
// state-invalid Handle refuses every call with ErrPluginUnavailable
// without touching the transport.
func (h *Handle) Call(ctx context.Context, method string, params []byte) ([]byte, error) {
	if h.state.get() == stateInvalid {
		return nil, wrapUnavailable(h.Manifest.Name, h.state.restartCountSnapshot())
	}
	h.mu.RLock()
	t := h.transport
	h.mu.RUnlock()
	return t.Call(ctx, method, params)
}

// State reports whether the Handle is still running or has been marked
// state-invalid after exhausting its restart budget.
func (h *Handle) State() string {
	if h.state.get() == stateInvalid {
		return "invalid"
	}
	return "running"
}

// swapTransport installs t as the transport future Call invocations use,
// following a successful restart.
func (h *Handle) swapTransport(t *Transport) {
	h.mu.Lock()
	h.transport = t
	h.mu.Unlock()
}

// restartCountSnapshot reads the current restart count without mutating
// it, for inclusion in the unavailable-error message.
func (s *stateBox) restartCountSnapshot() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.restartCount
}
