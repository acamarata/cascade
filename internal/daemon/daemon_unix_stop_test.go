//go:build !windows

package daemon

// Purpose: Stop's SIGTERM-then-bounded-SIGKILL escalation, including a
//   daemon that "refuses to exit" and one that never exits at all. Split
//   from daemon_unix_test.go under R-14.117/R-14.133 (Art.10.3's 300-line
//   file cap) — same package, no behaviour change.
// SPORT: internal/daemon (ADD, per T-2 sport_updates).

import (
	"context"
	"errors"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// stopOptsFor builds StopOptions for pid 555, recording every signal sent
// into signals and reporting SocketGone true only once polled more than
// socketGoneAfter times — the shape every Stop escalation test below
// drives with a different socketGoneAfter.
func stopOptsFor(pidPath string, signals *[]syscall.Signal, socketGoneAfter int) StopOptions {
	polls := 0
	return StopOptions{
		PIDPath: pidPath,
		Prober:  fakeProber{alive: map[int]bool{555: true}},
		Signal: func(_ int, sig syscall.Signal) error {
			*signals = append(*signals, sig)
			return nil
		},
		SocketGone: func() bool {
			polls++
			return polls > socketGoneAfter
		},
		Sleep: noopSleep,
	}
}

func TestStop_NothingRunning_IsIdempotent(t *testing.T) {
	var signals []syscall.Signal
	res, err := Stop(context.Background(), StopOptions{
		PIDPath:    filepath.Join(t.TempDir(), "daemon.pid"),
		Prober:     fakeProber{},
		Signal:     func(_ int, sig syscall.Signal) error { signals = append(signals, sig); return nil },
		SocketGone: func() bool { return true },
		Sleep:      noopSleep,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.WasRunning {
		t.Error("WasRunning = true, want false (nothing to stop)")
	}
	if len(signals) != 0 {
		t.Errorf("signals sent = %v, want none", signals)
	}
}

func TestStop_GracefulExit_SIGTERMOnly(t *testing.T) {
	dir := t.TempDir()
	pidPath := filepath.Join(dir, "daemon.pid")
	if err := writePIDFile(pidPath, pidRecord{PID: 555}); err != nil {
		t.Fatal(err)
	}
	var signals []syscall.Signal
	res, err := Stop(context.Background(), stopOptsFor(pidPath, &signals, 2))
	if err != nil {
		t.Fatal(err)
	}
	if !res.WasRunning || res.Escalated {
		t.Errorf("res = %+v, want WasRunning=true Escalated=false", res)
	}
	if len(signals) != 1 || signals[0] != syscall.SIGTERM {
		t.Errorf("signals = %v, want exactly [SIGTERM]", signals)
	}
	if _, ok, _ := readPIDFile(pidPath); ok {
		t.Error("pidfile still present after a successful stop")
	}
}

// TestStop_RefusesToExit_EscalatesToSIGKILL is this ticket's required "test
// a daemon that refuses to exit" case: SocketGone never becomes true
// within the SIGTERM grace window, so Stop must escalate to SIGKILL and
// succeed only once that also is (bounded) polled true.
func TestStop_RefusesToExit_EscalatesToSIGKILL(t *testing.T) {
	dir := t.TempDir()
	pidPath := filepath.Join(dir, "daemon.pid")
	if err := writePIDFile(pidPath, pidRecord{PID: 555}); err != nil {
		t.Fatal(err)
	}
	var signals []syscall.Signal
	// socketGoneAfter = stopGraceAttempts + 2: never gone during the grace
	// window (stopGraceAttempts polls), only after a couple more post-SIGKILL.
	res, err := Stop(context.Background(), stopOptsFor(pidPath, &signals, stopGraceAttempts+2))
	if err != nil {
		t.Fatal(err)
	}
	if !res.WasRunning || !res.Escalated {
		t.Errorf("res = %+v, want WasRunning=true Escalated=true", res)
	}
	if len(signals) != 2 || signals[0] != syscall.SIGTERM || signals[1] != syscall.SIGKILL {
		t.Errorf("signals = %v, want [SIGTERM, SIGKILL]", signals)
	}
}

// TestStop_NeverExits_BoundedTimeoutError proves the SIGKILL escalation
// itself is bounded: a process that ignores even SIGKILL delivery (e.g. the
// signal call succeeds but SocketGone never reports true) makes Stop return
// a typed error rather than hang forever.
func TestStop_NeverExits_BoundedTimeoutError(t *testing.T) {
	dir := t.TempDir()
	pidPath := filepath.Join(dir, "daemon.pid")
	if err := writePIDFile(pidPath, pidRecord{PID: 555}); err != nil {
		t.Fatal(err)
	}
	var signals []syscall.Signal
	_, err := Stop(context.Background(), stopOptsFor(pidPath, &signals, 1<<30))
	if !cascade.HasKind(err, cascade.KindTimeout) {
		t.Fatalf("err = %v, want KindTimeout", err)
	}
	if len(signals) != 2 {
		t.Errorf("signals = %v, want exactly [SIGTERM, SIGKILL] before giving up", signals)
	}
}

func TestStop_SignalError_IsSurfaced(t *testing.T) {
	dir := t.TempDir()
	pidPath := filepath.Join(dir, "daemon.pid")
	if err := writePIDFile(pidPath, pidRecord{PID: 555}); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("boom")
	_, err := Stop(context.Background(), StopOptions{
		PIDPath:    pidPath,
		Prober:     fakeProber{alive: map[int]bool{555: true}},
		Signal:     func(int, syscall.Signal) error { return boom },
		SocketGone: func() bool { return false },
		Sleep:      noopSleep,
	})
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("err = %v, want KindUnavailable", err)
	}
}
