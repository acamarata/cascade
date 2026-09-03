//go:build !windows

package daemon

// Purpose: Stop and Restart — `cascade daemon stop`/`restart`'s
//   implementation: Stop signals the pidfile's PID with SIGTERM and waits
//   (bounded) for the socket path to be removed (the daemon's own Run
//   cleanup, lifecycle_unix.go), escalating to SIGKILL if the grace window
//   elapses with no exit, and is idempotent against an already-stopped
//   daemon. Restart is Stop then Start, sequenced so the old daemon is
//   fully gone — socket removed, pidfile removed, process confirmed dead —
//   before the new one is spawned: no window where two daemons hold the
//   socket at once. Split from lifecycle_unix.go under R-14.117.
// Inputs: StopOptions — an injected Signal func (production: os.
//   FindProcess + Signal; tests: a fake that can simulate "ignores
//   SIGTERM"), SocketGone (poll predicate), and Sleep (backoff delay,
//   faked in tests, R-14.136).
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
	PIDPath    string
	Prober     ProcessProber
	Signal     func(pid int, sig syscall.Signal) error
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
	if pollGone(opts, stopGraceAttempts) {
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
	if pollGone(opts, stopKillAttempts) {
		_ = removePIDFile(opts.PIDPath)
		return StopResult{WasRunning: true, Escalated: true}, nil
	}

	return StopResult{}, cascade.Newf(cascade.KindTimeout,
		"daemon: pid=%d did not exit after SIGTERM and SIGKILL", rec.PID)
}

func pollGone(opts StopOptions, attempts int) bool {
	for i := 0; i < attempts; i++ {
		if opts.SocketGone() {
			return true
		}
		opts.Sleep(stopPollInterval)
	}
	return opts.SocketGone()
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

// Restart stops the running daemon (fully — socket and pidfile confirmed
// gone) and only then starts a new one. This ordering is the whole
// guarantee: because Stop does not return until the old daemon's socket
// and pidfile are gone (or SIGKILL forced it), Start's own pidfile check
// can never observe a live old daemon, so two daemons never hold the
// socket at once. The cost is a real (bounded) gap with no daemon serving
// between Stop returning and Start's readiness probe passing — unavoidable
// for a single-instance daemon model, since the alternative (start-before-
// stop) is exactly the overlap this ordering exists to prevent.
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
