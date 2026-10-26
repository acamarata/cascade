package update

// Purpose: hand the running daemon to the swapped binary: one upgrade
//   hand-off signal (SIGUSR2, daemon.UpgradeSignal) so its UpgradeManager
//   drains and exec-relaunches, then wait until status.get reports the
//   candidate version.
// Inputs: DrainDeps: the pidfile status, the signal sender, the version
//   probe, a Clock, the poll interval and the timeout.
// Outputs: whether a daemon was signalled; nil once the version matches.
// Constraints: exactly one Signal call, only when a daemon is running; the
//   sender is bound to the hand-off signal by the caller (UpgradeSignaller
//   on unix), never SIGTERM or SIGKILL. WaitVersion polls every Poll (250
//   ms) until Timeout (30 s) by the injected Clock and refuses with
//   KindUnavailable naming `cascade daemon start`. Each probe runs under a
//   context bounded by the remaining timeout, and an answer that arrives
//   after the deadline (by the Clock) is not accepted.
// SPORT: internal/update Drain/ADDED, WaitVersion/ADDED.

import (
	"context"
	"strings"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// The WaitVersion defaults.
const (
	DefaultPoll    = 250 * time.Millisecond
	DefaultTimeout = 30 * time.Second
)

// DrainDeps carries every daemon input Drain and WaitVersion use.
type DrainDeps struct {
	Status  func(context.Context) (running bool, pid int, err error)
	Signal  func(pid int) error
	Version func(context.Context) (string, error)
	Clock   runtime.Clock
	Poll    time.Duration
	Timeout time.Duration
}

// Drain sends the hand-off signal once when a daemon is running.
func Drain(ctx context.Context, d DrainDeps) (signalled bool, err error) {
	if d.Status == nil || d.Signal == nil {
		return false, cascade.New(cascade.KindInternal, "update: drain needs a status probe and a signal sender")
	}
	running, pid, err := d.Status(ctx)
	if err != nil {
		return false, cascade.Wrap(cascade.KindUnavailable, err, "update: read the daemon status")
	}
	if !running {
		return false, nil
	}
	if err := d.Signal(pid); err != nil {
		return false, cascade.Wrapf(cascade.KindUnavailable, err, "update: signal the daemon (pid %d) to hand off", pid)
	}
	return true, nil
}

// WaitVersion polls the daemon until it reports want.
func WaitVersion(ctx context.Context, d DrainDeps, want string) error {
	if d.Version == nil || d.Clock == nil {
		return cascade.New(cascade.KindInternal, "update: waiting for the daemon needs a version probe and a clock")
	}
	poll, timeout := d.Poll, d.Timeout
	if poll <= 0 {
		poll = DefaultPoll
	}
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	deadline := d.Clock.Now().Add(timeout)
	want = strings.TrimPrefix(want, "v")
	for {
		remaining := deadline.Sub(d.Clock.Now())
		if remaining <= 0 {
			return cascade.Newf(cascade.KindUnavailable,
				"update: the daemon did not come back on version %s within %s; "+
					"check `cascade daemon status` and run `cascade daemon start` if it is not running", want, timeout)
		}
		got, err := probeVersion(ctx, d.Version, remaining)
		if err == nil && strings.TrimPrefix(got, "v") == want && d.Clock.Now().Before(deadline) {
			return nil
		}
		if err := waitPoll(ctx, poll); err != nil {
			return err
		}
	}
}

// probeVersion runs one version probe bounded by the remaining timeout.
func probeVersion(ctx context.Context, probe func(context.Context) (string, error), remaining time.Duration) (string, error) {
	pctx, cancel := context.WithTimeout(ctx, remaining)
	defer cancel()
	return probe(pctx)
}

// waitPoll waits one interval or until ctx ends.
func waitPoll(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return cascade.Wrap(cascade.KindCanceled, ctx.Err(), "update: waiting for the daemon")
	case <-timer.C:
		return nil
	}
}
