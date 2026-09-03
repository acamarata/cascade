//go:build !windows

package daemon

// Purpose: Start — `cascade daemon start`'s background self-relaunch:
//   idempotent (an already-running daemon, detected via pidfile liveness,
//   is a no-op that reports the existing PID rather than spawning a
//   second one) and readiness-gated (does not return to the caller until
//   the relaunched daemon's socket is actually accepting connections, or a
//   bounded number of backoff attempts is exhausted). Split from
//   lifecycle_unix.go under R-14.117 (300-line file cap).
// Inputs: StartOptions — an injected ProcessProber (liveness), Spawn
//   (how to launch "daemon run" in the background — DefaultSpawn in
//   production, a fake in tests), ReadyProbe (how to tell the new daemon
//   is actually serving), and Sleep (the backoff delay — production
//   time.Sleep, tests a no-op so no test ever sleeps for real, R-14.136).
// Outputs: StartResult{AlreadyRunning, PID}; a typed error if the spawn
//   fails or readiness is never reached within the bounded attempt budget.
// Constraints: Art.7.1 — every seam a test needs to control (liveness,
//   spawning, readiness, delay) is a struct field, never a package-level
//   default reached directly.
// SPORT: internal/daemon (ADD, per T-2 sport_updates).

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// startBackoffBase/Cap/MaxAttempts bound Start's readiness poll: capped
// exponential backoff (20ms doubling to 1s) for at most 12 attempts — a
// deterministic, sleep-free-in-tests bound (each attempt is one Sleep call,
// faked in tests) rather than a wall-clock timeout.
const (
	startBackoffBase = 20 * time.Millisecond
	startBackoffCap  = 1 * time.Second
	startMaxAttempts = 12
)

// StartResult reports what Start did.
type StartResult struct {
	AlreadyRunning bool
	PID            int
}

// StartOptions carries every input Start needs.
type StartOptions struct {
	PIDPath    string
	Prober     ProcessProber
	Spawn      func(ctx context.Context) (pid int, err error)
	ReadyProbe func() bool
	Sleep      func(time.Duration)
	Logger     *slog.Logger
}

// Start makes a background daemon exist and be ready, idempotently.
func Start(ctx context.Context, opts StartOptions) (StartResult, error) {
	rec, ok, err := readPIDFile(opts.PIDPath)
	if err != nil {
		return StartResult{}, err
	}
	switch classifyPID(rec, ok, opts.Prober) {
	case livenessNotRunning:
		// Nothing recorded at all: fall straight through to spawning.
	case livenessRunning:
		logInfo(opts.Logger, "daemon: start: already running", "pid", rec.PID)
		return StartResult{AlreadyRunning: true, PID: rec.PID}, nil
	case livenessStale, livenessRecycled:
		logInfo(opts.Logger, "daemon: start: removing stale pidfile", "pid", rec.PID)
		if err := removePIDFile(opts.PIDPath); err != nil {
			return StartResult{}, err
		}
	}

	pid, err := opts.Spawn(ctx)
	if err != nil {
		return StartResult{}, cascade.Wrap(cascade.KindUnavailable, err, "daemon: spawn")
	}

	if !waitReady(opts) {
		return StartResult{}, cascade.Newf(cascade.KindTimeout,
			"daemon: started pid=%d but socket never became ready", pid)
	}
	return StartResult{PID: pid}, nil
}

// waitReady polls opts.ReadyProbe with capped exponential backoff.
func waitReady(opts StartOptions) bool {
	delay := startBackoffBase
	for attempt := 0; attempt < startMaxAttempts; attempt++ {
		if opts.ReadyProbe() {
			return true
		}
		opts.Sleep(delay)
		delay *= 2
		if delay > startBackoffCap {
			delay = startBackoffCap
		}
	}
	return opts.ReadyProbe()
}

func logInfo(log *slog.Logger, msg, key string, val int) {
	if log == nil {
		return
	}
	log.Info(msg, slog.Int(key, val))
}

// DefaultSpawn returns a StartOptions.Spawn implementation that relaunches
// execPath with args ("daemon", "run", ...) as a detached background
// process (its own session, so it survives the launching CLI process
// exiting), with stdout/stderr appended to logPath.
func DefaultSpawn(execPath string, args []string, logPath string) func(context.Context) (int, error) {
	return func(_ context.Context) (int, error) {
		logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return 0, fmt.Errorf("open daemon log: %w", err)
		}
		cmd := exec.Command(execPath, args...)
		cmd.Stdout = logFile
		cmd.Stderr = logFile
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		startErr := cmd.Start()
		// The child's exec duplicated logFile's fd; the parent's own copy
		// is closed either way, success or failure.
		_ = logFile.Close()
		if startErr != nil {
			return 0, startErr
		}
		// Captured before Release: os.Process.Release sets p.Pid to -1 on
		// unix once called (a real Go stdlib behaviour, not documented in
		// the one-line doc comment — caught here because this ticket
		// asserts on the process's REAL exit/PID observable behaviour,
		// R-14.123, rather than trusting the call to be side-effect-free).
		pid := cmd.Process.Pid
		// Released, not Waited: this is a detached background process the
		// launching CLI does not reap.
		_ = cmd.Process.Release()
		return pid, nil
	}
}
