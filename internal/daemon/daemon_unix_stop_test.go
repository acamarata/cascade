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
	"log/slog"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// exitingProber is a ProcessProber whose process stays alive for the first
// aliveFor liveness probes and is gone from then on: the seam every Stop
// escalation test below drives, now that Stop's exit predicate is the
// PROCESS being gone rather than the socket file being unlinked (the
// socket vanishes while the old daemon still owns its store, so it never
// meant "gone"; see pollGone).
type exitingProber struct {
	pid      int
	aliveFor int
	probes   *int
}

func (p exitingProber) IsAlive(pid int) bool {
	*p.probes++
	return pid == p.pid && *p.probes <= p.aliveFor
}

// StartTime reports unknown, the honest answer for a fake that models no
// process table: classifyPID documents unknown as "IsAlive decides".
func (exitingProber) StartTime(int) (time.Time, bool) { return time.Time{}, false }

// stopOptsFor builds StopOptions for pid 555, recording every signal sent
// into signals, whose process stays alive for aliveFor liveness probes.
// SocketGone reports the socket already unlinked from the very first poll:
// that is exactly the production ordering (Run's deferred cleanup unlinks
// the socket before the composition root's defers close the store), and
// Stop must nonetheless keep waiting for the process itself.
func stopOptsFor(pidPath string, signals *[]syscall.Signal, aliveFor int) StopOptions {
	probes := 0
	return StopOptions{
		PIDPath: pidPath,
		Prober:  exitingProber{pid: 555, aliveFor: aliveFor, probes: &probes},
		Signal: func(_ int, sig syscall.Signal) error {
			*signals = append(*signals, sig)
			return nil
		},
		SocketGone: func() bool { return true },
		Sleep:      noopSleep,
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
	// aliveFor = stopGraceAttempts + 2: the process is still alive through
	// the whole grace window, exiting only a couple of probes after SIGKILL.
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

// TestStop_CorruptPIDFile_ReadErrorIsSurfaced covers Stop's readPIDFile
// error branch (distinct from the "not present at all" and "process gone"
// cases above): a pidfile that exists but is not valid JSON must surface
// readPIDFile's KindIntegrity error rather than being treated as
// not-running.
func TestStop_CorruptPIDFile_ReadErrorIsSurfaced(t *testing.T) {
	dir := t.TempDir()
	pidPath := filepath.Join(dir, "daemon.pid")
	if err := writeFileHelper(pidPath, "not json"); err != nil {
		t.Fatal(err)
	}
	_, err := Stop(context.Background(), StopOptions{
		PIDPath:    pidPath,
		Prober:     fakeProber{},
		Signal:     func(int, syscall.Signal) error { return nil },
		SocketGone: func() bool { return true },
		Sleep:      noopSleep,
	})
	if !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("err = %v, want KindIntegrity", err)
	}
}

// TestStop_SIGKILLSignalError_IsSurfaced covers the SIGKILL-send error
// branch, distinct from TestStop_SignalError_IsSurfaced below which only
// exercises the SIGTERM-send error (the earlier of the two identical-
// shaped branches). The SIGTERM must succeed and the grace window must
// elapse (SocketGone stays false) so Stop actually reaches the SIGKILL
// call before failing it.
func TestStop_SIGKILLSignalError_IsSurfaced(t *testing.T) {
	dir := t.TempDir()
	pidPath := filepath.Join(dir, "daemon.pid")
	if err := writePIDFile(pidPath, pidRecord{PID: 555}); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("boom")
	_, err := Stop(context.Background(), StopOptions{
		PIDPath: pidPath,
		Prober:  fakeProber{alive: map[int]bool{555: true}},
		Signal: func(_ int, sig syscall.Signal) error {
			if sig == syscall.SIGKILL {
				return boom
			}
			return nil
		},
		SocketGone: func() bool { return false },
		Sleep:      noopSleep,
	})
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("err = %v, want KindUnavailable", err)
	}
}

// TestStop_RefusesToExit_WithLogger_WarnsOnEscalation proves the
// `opts.Logger != nil` branch: when a Logger is supplied, escalating to
// SIGKILL must emit the documented warning line naming the pid. The other
// escalation tests above pass no Logger and so never exercise this branch.
func TestStop_RefusesToExit_WithLogger_WarnsOnEscalation(t *testing.T) {
	dir := t.TempDir()
	pidPath := filepath.Join(dir, "daemon.pid")
	if err := writePIDFile(pidPath, pidRecord{PID: 555}); err != nil {
		t.Fatal(err)
	}
	log, records := newRecordingLogger()
	var signals []syscall.Signal
	opts := stopOptsFor(pidPath, &signals, stopGraceAttempts+2)
	opts.Logger = log
	res, err := Stop(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Escalated {
		t.Fatalf("res = %+v, want Escalated=true", res)
	}
	var sawWarn bool
	for _, r := range *records {
		if r.Level == slog.LevelWarn && r.Message == "daemon: stop: SIGTERM grace window elapsed, escalating to SIGKILL" {
			sawWarn = true
		}
	}
	if !sawWarn {
		t.Error("did not see the SIGKILL-escalation warning log line")
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
