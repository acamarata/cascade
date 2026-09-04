//go:build !windows

package daemon

// Purpose: Stop and Restart — `cascade daemon stop`/`restart`'s
//   implementation: Stop signals the pidfile's PID with SIGTERM and waits
//   (bounded) for the socket path to be removed (the daemon's own Run
//   cleanup, lifecycle_unix.go), escalating to SIGKILL if the grace window
//   elapses with no exit, and is idempotent against an already-stopped
//   daemon. Restart is Stop then Start, sequenced so the old daemon's
//   PROCESS has exited before the new one is spawned. Process exit, not
//   socket removal, is the guarantee that matters: Run unlinks the socket
//   from its own deferred cleanup, while the SQLite handle, the WAL
//   checkpoint and the advisory lock are owned by the composition root
//   OUTSIDE Run and are released by defers that run after Run returns. An
//   exited process, in contrast, has had every descriptor and every file
//   lock released by the kernel, on a clean exit and on a SIGKILL alike.
//   Split from lifecycle_unix.go under R-14.117.
// Inputs: StopOptions — an injected Signal func (production: os.
//   FindProcess + Signal; tests: a fake that can simulate "ignores
//   SIGTERM"), a ProcessProber (the exit predicate), SocketGone (a
//   secondary observation, reported when a killed daemon leaves its
//   socket file behind), and Sleep (backoff delay, faked in tests,
//   R-14.136).
// Outputs: StopResult{WasRunning, Escalated}; a typed KindTimeout error if
//   the process survives both SIGTERM and SIGKILL within the bound.
// SPORT: internal/daemon (ADD, per T-2 sport_updates).

import (
	"context"
	"log/slog"
	"os"
	"syscall"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// stopPollInterval/GraceAttempts/KillAttempts: the bounded escalation
// schedule. Grace window = GraceAttempts * stopPollInterval before
// SIGKILL; kill window = KillAttempts * stopPollInterval before giving up.
// Both are attempt-counted (not wall-clock), so Sleep can be a real delay
// in production and a no-op in tests without changing the bound.
const (
	stopPollInterval  = 50 * time.Millisecond
	stopGraceAttempts = 40 // 50ms * 40 = 2s of real time in production
	stopKillAttempts  = 20 // 1s more after SIGKILL
)

// StopResult reports what Stop did.
type StopResult struct {
	// WasRunning is false when there was nothing to stop (idempotent).
	WasRunning bool
	// Escalated is true when SIGTERM alone was not enough and SIGKILL was
	// sent.
	Escalated bool
}

// StopOptions carries every input Stop needs.
type StopOptions struct {
	PIDPath string
	Prober  ProcessProber
	Signal  func(pid int, sig syscall.Signal) error
	// SocketGone reports whether the daemon's socket path is gone. It is
	// NOT the exit predicate (see pollGone): a socket file outliving a
	// SIGKILLed daemon is stale, and listenSocket reclaims it on the next
	// bind. Stop consults it only to log that leftover.
	SocketGone func() bool
	Sleep      func(time.Duration)
	Logger     *slog.Logger
}

// Stop signals the running daemon and waits for it to exit.
func Stop(_ context.Context, opts StopOptions) (StopResult, error) {
	rec, ok, err := readPIDFile(opts.PIDPath)
	if err != nil {
		return StopResult{}, err
	}
	state := classifyPID(rec, ok, opts.Prober)
	if state == livenessNotRunning || state == livenessStale || state == livenessRecycled {
		_ = removePIDFile(opts.PIDPath)
		return StopResult{WasRunning: false}, nil
	}

	if err := opts.Signal(rec.PID, syscall.SIGTERM); err != nil {
		return StopResult{}, cascade.Wrap(cascade.KindUnavailable, err, "daemon: send SIGTERM")
	}
	if pollGone(opts, rec, stopGraceAttempts) {
		_ = removePIDFile(opts.PIDPath)
		return StopResult{WasRunning: true}, nil
	}

	if opts.Logger != nil {
		opts.Logger.Warn("daemon: stop: SIGTERM grace window elapsed, escalating to SIGKILL",
			slog.Int("pid", rec.PID))
	}
	if err := opts.Signal(rec.PID, syscall.SIGKILL); err != nil {
		return StopResult{}, cascade.Wrap(cascade.KindUnavailable, err, "daemon: send SIGKILL")
	}
	if pollGone(opts, rec, stopKillAttempts) {
		_ = removePIDFile(opts.PIDPath)
		return StopResult{WasRunning: true, Escalated: true}, nil
	}

	return StopResult{}, cascade.Newf(cascade.KindTimeout,
		"daemon: pid=%d did not exit after SIGTERM and SIGKILL", rec.PID)
}

// pollGone waits, bounded, for the signalled daemon to be gone in the only
// sense a replacement can rely on: its process has exited (or the PID has
// since been recycled by an unrelated process, which equally means the old
// daemon is no longer running). The socket file's disappearance is NOT
// that signal — Run unlinks the socket from a defer that runs while the
// process still holds the store, so a socket-only predicate lets Restart
// spawn a replacement that then cannot take the store.
func pollGone(opts StopOptions, rec pidRecord, attempts int) bool {
	for i := 0; i < attempts; i++ {
		if processGone(opts, rec) {
			return true
		}
		opts.Sleep(stopPollInterval)
	}
	return processGone(opts, rec)
}

// processGone reports whether rec's process is no longer the running
// daemon, and notes the stale socket file a SIGKILLed daemon leaves behind
// (its own cleanup never ran, so nothing unlinked it; the next bind
// reclaims it, see listenSocket).
func processGone(opts StopOptions, rec pidRecord) bool {
	if classifyPID(rec, true, opts.Prober) == livenessRunning {
		return false
	}
	if opts.Logger != nil && opts.SocketGone != nil && !opts.SocketGone() {
		opts.Logger.Warn("daemon: stop: process exited leaving a stale socket file",
			slog.Int("pid", rec.PID))
	}
	return true
}

// RestartResult reports Stop's and Start's combined outcome.
type RestartResult struct {
	StopResult  StopResult
	StartResult StartResult
}

// RestartOptions carries Stop's and Start's option sets together.
type RestartOptions struct {
	Stop  StopOptions
	Start StartOptions
}

// Restart stops the running daemon and only then starts a new one. What
// "stopped" means here is the whole guarantee: Stop does not return until
// the old daemon's PROCESS has exited, so by the time Start spawns, the
// kernel has already released every descriptor and every file lock that
// process held — its socket, its SQLite database handle, its WAL and its
// advisory lock. A predicate that only watched the socket path would not
// carry that: the socket is unlinked from a defer inside Run, while the
// store is closed by defers in the composition root that run afterwards,
// so the replacement could be spawned into a store the old process still
// owned and fail its readiness budget. The cost is a real (bounded) gap
// with no daemon serving between Stop returning and Start's readiness
// probe passing — unavoidable for a single-instance daemon model, since
// the alternative (start-before-stop) is exactly the overlap this
// ordering exists to prevent.
func Restart(ctx context.Context, opts RestartOptions) (RestartResult, error) {
	stopRes, err := Stop(ctx, opts.Stop)
	if err != nil {
		return RestartResult{}, err
	}
	startRes, err := Start(ctx, opts.Start)
	if err != nil {
		return RestartResult{StopResult: stopRes}, err
	}
	return RestartResult{StopResult: stopRes, StartResult: startRes}, nil
}

// DefaultSignal is the production Signal implementation.
func DefaultSignal(pid int, sig syscall.Signal) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return proc.Signal(sig)
}
