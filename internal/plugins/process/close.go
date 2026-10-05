//go:build !windows

// Purpose: end a plugin process on purpose, and bound how long that takes:
//
//	Handle.Close (stdin EOF, SIGTERM to the process group, StopGrace,
//	SIGKILL to the group, reap), Handle.Alive, and the execCommander
//	adapter that puts every real child in its own process group.
//
// Inputs: a running Handle and the caller's deadline.
// Outputs: nil once the child and every process the monitor started are
//
//	reaped; KindUnavailable naming the plugin if the deadline passes first.
//
// Constraints: a signal always targets the child's process group through
//
//	pkg/procgroup, so a grandchild that inherited stdout dies with it. A
//	group whose leader was already waited for is never signalled, because
//	its pgid can be reused (R134). Residual, recorded: on darwin a
//	SIGKILLed daemon leaves its children running until they read EOF on
//	stdin (no parent-death signal there; pkg/procgroup sets Pdeathsig on
//	linux only). Windows never launches (runtime_windows.go), so this
//	file is unix-only.
//
// SPORT: internal/plugins/process close (ADD) — P1-PLG-09.

package process

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/procgroup"
)

// DefaultStopGrace is the SIGTERM-to-SIGKILL grace Close uses when
// ProcessRuntime.StopGrace is zero.
const DefaultStopGrace = 3 * time.Second

// execCommander adapts a real *exec.Cmd to Commander. Its Signal reaches
// the child's whole process group.
type execCommander struct{ *exec.Cmd }

// newExecCommander starts cmd in a new process group and makes the end of
// its context (daemon shutdown included) SIGKILL that whole group, not
// only the child os/exec would otherwise kill.
func newExecCommander(cmd *exec.Cmd) execCommander {
	cmd.SysProcAttr = procgroup.Attr()
	c := execCommander{cmd}
	cmd.Cancel = func() error { return c.Signal(syscall.SIGKILL) }
	return c
}

// Signal sends sig to the child's process group. A child not started, or
// already waited for, is not signalled: nil, never a reused pgid.
func (c execCommander) Signal(sig os.Signal) error {
	s, ok := sig.(syscall.Signal)
	if !ok {
		return cascade.Newf(cascade.KindInvalidInput, "process: unsupported signal %v", sig)
	}
	if c.Process == nil || leaderWaited(c.Process) {
		return nil
	}
	// Residual: Wait can reap between leaderWaited and the group signal,
	// allowing pid reuse in that window. Pre-reap SIGKILL in monitor and
	// abandonChild leaves the group empty before that reuse is possible.
	// Do not lock Signal against Wait: exec.Cmd.Wait can wait for Cancel.
	return procgroup.Signal(c.Process.Pid, s)
}

// leaderWaited reports whether p was already waited for. It asks p itself
// instead of reading Cmd.ProcessState, which Wait writes concurrently with
// os/exec's Cancel goroutine (a data race): os.Process marks itself done
// before it reaps, and Signal reports ErrProcessDone from then on.
func leaderWaited(p *os.Process) bool {
	return errors.Is(p.Signal(syscall.Signal(0)), os.ErrProcessDone)
}

// resolvedStopGrace returns rt.StopGrace, defaulting to DefaultStopGrace.
func (rt *ProcessRuntime) resolvedStopGrace() time.Duration {
	if rt.StopGrace > 0 {
		return rt.StopGrace
	}
	return DefaultStopGrace
}

// Close stops the plugin and waits, bounded by ctx, until it is reaped.
// It ends the lifetime first, so the monitor never respawns; then closes
// stdin, sends SIGTERM, waits StopGrace, and sends SIGKILL. Close is
// idempotent: a second call returns the first call's result.
func (h *Handle) Close(ctx context.Context) error {
	h.closeOnce.Do(func() { h.closeErr = h.terminate(ctx) })
	return h.closeErr
}

// terminate is Close's body, run once.
func (h *Handle) terminate(ctx context.Context) error {
	h.mu.Lock()
	if h.runCancel != nil {
		h.runCancel()
	}
	cmd, reaped, t, done := h.cmd, h.reaped, h.transport, h.monitorDone
	h.mu.Unlock()
	if cmd == nil || done == nil {
		return nil // a Handle that never had a running child
	}
	if t != nil {
		_ = t.Close()
	}
	_ = cmd.Signal(syscall.SIGTERM)
	if !waitFor(ctx, reaped, h.stopGrace) {
		_ = cmd.Signal(syscall.SIGKILL)
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return cascade.Wrapf(cascade.KindUnavailable, ctx.Err(),
			"process: plugin %q was not reaped before the close deadline", h.Manifest.Name)
	}
}

// Alive reports whether h's current child has not been reaped and its
// transport is still reading.
func (h *Handle) Alive() bool {
	h.mu.RLock()
	reaped, t := h.reaped, h.transport
	h.mu.RUnlock()
	if reaped == nil || t == nil {
		return false
	}
	return !closed(reaped) && !closed(t.Done())
}

// abandonChild ends a child that will never be installed (failed handshake,
// or a respawn that lost the race against Close): stdin EOF first, so a
// well-behaved plugin exits on its own, then SIGKILL to its group if it is
// still running after grace. It returns once the child is reaped.
func abandonChild(cmd Commander, t *Transport, grace time.Duration) {
	_ = t.Close()
	reaped := make(chan struct{})
	go func() {
		awaitDrained(t)
		// The unreaped leader reserves the pgid (even as a zombie), so
		// killing members that outlived it is safe from group-id reuse.
		_ = cmd.Signal(syscall.SIGKILL)
		_ = cmd.Wait()
		close(reaped)
	}()
	if !waitFor(context.Background(), reaped, grace) {
		_ = cmd.Signal(syscall.SIGKILL)
		<-reaped
	}
}

// waitFor reports whether ch closed within d (and before ctx ended). A nil
// ch never closes.
func waitFor(ctx context.Context, ch <-chan struct{}, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ch:
		return true
	case <-timer.C:
		return false
	case <-ctx.Done():
		return false
	}
}

// closed reports whether ch is closed, without blocking.
func closed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}
