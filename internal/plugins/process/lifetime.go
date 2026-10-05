//go:build !windows

// Purpose: separates a plugin process's LIFETIME from the deadline that
//
//	bounds its startup handshake, and spawns the Commander against the
//	former.
//
// Inputs: the lifetime context a Launch caller owns, and a Manifest.
//
// Outputs: a started Commander plus the Transport wrapping its stdio.
//
// Constraints: the context handed to the CommandFactory becomes the
//
//	child process's kill switch — defaultCommandFactory builds an
//	os/exec.CommandContext, which SIGKILLs the child the moment that
//	context is done. Conflating it with the startup deadline is therefore
//	not a timeout bug but a lifetime bug: Launch's `defer cancel()` fires
//	as soon as Launch returns, so every plugin was killed immediately on
//	launch, before it could issue a single host call. The conformance
//	suite (internal/plugins/conformance) only ever passed by winning the
//	race between the shell's second write and Launch's return; under
//	whole-tree `go test ./...` load the Go side wins and every fixture
//	that needs a real host-boundary observation times out. spawnOne takes
//	only the lifetime context for exactly this reason; performHandshake
//	keeps the bounded one.
//
//	Two contexts hang off a Handle. lifetimeCtx is the caller's: every
//	child (initial and respawned) is built from it, so its end SIGKILLs
//	the child's process group. lifetime() is runCtx, derived from it and
//	also ended by Close: backoff, respawn handshakes and host-call
//	consumers run under it. Close ends runCtx first and signals the child
//	itself, so the child gets its SIGTERM grace instead of the instant
//	SIGKILL its own context would deliver.
//
// SPORT: internal/plugins/process lifetime (ADD) — P1-E15-W4-S31-T3; lifetime-bound respawn (CHANGE) — P1-PLG-09.

package process

import (
	"context"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// spawnOne builds a Commander from manifest, opens its stdio pipes,
// starts it, and wraps its stdio in a Transport. lifetimeCtx governs how
// long the child may run and MUST NOT carry the startup deadline: see
// this file's Constraints.
func (rt *ProcessRuntime) spawnOne(lifetimeCtx context.Context, manifest Manifest) (Commander, *Transport, error) {
	cmd := rt.factory()(lifetimeCtx, manifest)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, cascade.Wrap(cascade.KindUnavailable, err, "process: opening plugin stdin")
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, cascade.Wrap(cascade.KindUnavailable, err, "process: opening plugin stdout")
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, cascade.Wrap(cascade.KindUnavailable, err, "process: starting plugin process")
	}
	return cmd, NewTransport(stdin, stdout, rt.StartupTimeout), nil
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
	awaitDrained(t)
}

// awaitDrained waits, at most DrainGrace, for t's reads to finish.
func awaitDrained(t *Transport) {
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

// lifetime returns h's run context (see this file's Constraints), falling
// back to the caller's lifetime and then to context.Background for a
// Handle built without one (every test constructing a Handle literal): a
// zero lifetime must mean "not bounded", never a nil context that panics.
func (h *Handle) lifetime() context.Context {
	if h.runCtx != nil {
		return h.runCtx
	}
	return h.childCtx()
}

// childCtx is the context every child of h is built from: the caller's
// lifetime, never runCtx (see this file's Constraints).
func (h *Handle) childCtx() context.Context {
	if h.lifetimeCtx == nil {
		return context.Background()
	}
	return h.lifetimeCtx
}

// respawnOrFail runs one respawn for the monitor. A failed attempt yields
// failedWaiter (an immediate synthetic exit) so the restart count still
// advances; an ended lifetime then stops the loop on its next check.
func (rt *ProcessRuntime) respawnOrFail(manifest Manifest, h *Handle) (Waiter, chan struct{}) {
	next, reaped, err := rt.respawn(manifest, h)
	if err != nil {
		return failedWaiter{}, nil
	}
	return next, reaped
}

// respawn attempts one relaunch: a fresh Commander, a handshake bounded by
// h.lifetime(), and (on success) an atomic install as h's current child.
// It starts nothing once the lifetime has ended, and a child that won the
// race against Close is killed and reaped here rather than left running.
func (rt *ProcessRuntime) respawn(manifest Manifest, h *Handle) (Commander, chan struct{}, error) {
	life := h.lifetime()
	if err := life.Err(); err != nil {
		return nil, nil, cascade.Wrapf(cascade.KindUnavailable, err, "process: plugin %q lifetime ended; not respawning", manifest.Name)
	}
	startupCtx, cancel := context.WithTimeout(life, rt.resolvedStartupTimeout())
	defer cancel()
	cmd, transport, err := rt.spawnOne(h.childCtx(), manifest)
	if err != nil {
		return nil, nil, err
	}
	ack, err := performHandshake(startupCtx, transport, manifest.Name, manifest.minProtocolVersion())
	var reaped chan struct{}
	if err == nil {
		reaped, err = h.install(cmd, transport, ack)
	}
	if err != nil {
		grace := h.stopGrace
		if life.Err() != nil {
			grace = 0
		}
		abandonChild(cmd, transport, grace)
		return nil, nil, err
	}
	go rt.consumeHostCalls(life, transport)
	return cmd, reaped, nil
}

// install makes cmd and t h's current child, unless the lifetime ended
// first. It holds h.mu across the check and the swap, and Close ends the
// lifetime under the same lock, so a child is either installed before
// Close reads h.cmd or refused here.
func (h *Handle) install(cmd Commander, t *Transport, ack HelloAckMsg) (chan struct{}, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.lifetime().Err(); err != nil {
		return nil, cascade.Wrapf(cascade.KindUnavailable, err, "process: plugin %q closed during respawn", h.Manifest.Name)
	}
	h.Ack, h.transport, h.cmd = ack, t, cmd
	h.reaped = make(chan struct{})
	return h.reaped, nil
}

// failedWaiter is used when a respawn attempt itself fails before a
// process could even be started: it reports an immediate exit so the
// monitor loop's restart-count accounting still advances toward the
// budget rather than spinning without ever reaching state-invalid.
type failedWaiter struct{}

// Wait reports the synthetic exit for a respawn that never started.
func (failedWaiter) Wait() error {
	return cascade.New(cascade.KindUnavailable, "process: respawn attempt failed before the process started")
}
