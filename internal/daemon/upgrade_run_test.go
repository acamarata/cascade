//go:build !windows

package daemon

// Purpose: Run-level integration of the upgrade engine into
// lifecycle_unix.go's UpgradeSignal path — proves the wiring end to end,
// not just AttemptUpgrade in isolation. Split from upgrade_test.go purely
// for the 300-line file cap. No "net" import: Run takes a socket PATH and
// manages the listener itself (Art.7.2).
// SPORT: internal/daemon (ADD, per T-5 sport_updates).

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// stubExec replaces execFunc for one test with a recorder that returns
// result, and returns the call count and the last path it was handed.
func stubExec(t *testing.T, result error) (*atomic.Int32, *atomic.Value) {
	t.Helper()
	orig := execFunc
	t.Cleanup(func() { execFunc = orig })
	calls, path := &atomic.Int32{}, &atomic.Value{}
	execFunc = func(p string, _, _ []string) error {
		calls.Add(1)
		path.Store(p)
		return result
	}
	return calls, path
}

// upgradeRunOptions builds the RunOptions every Run-level upgrade test uses
// over h, with sigs as the signal source and m as the UpgradeManager.
func upgradeRunOptions(h *runHarness, sigs chan os.Signal, m *UpgradeManager) RunOptions {
	return RunOptions{
		Settings: Settings{SocketPath: h.socketPath, ShutdownGrace: time.Second},
		PIDPath:  h.pidPath,
		Logger:   h.log,
		Clock:    runtime.NewFixedClock(time.Now()),
		Signals:  sigs,
		Ready:    h.ready,
		Upgrade:  m,
	}
}

// waitRunReturn waits for Run's result and fails on a non-nil error or a
// Run that never returns.
func waitRunReturn(t *testing.T, done <-chan error, what string) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("Run did not return after %s", what)
	}
}

// TestRun_UpgradeOnSignal_Relaunches drives the real Run loop: UpgradeSignal
// with a replaced startup binary drains and execs that file through the
// stubbed execFunc, and Run returns nil (reachable only because the stub
// returns; a real successful exec never does).
func TestRun_UpgradeOnSignal_Relaunches(t *testing.T) {
	calls, path := stubExec(t, nil)
	bin := writeTempBinary(t, "release-1 bytes")
	pinStartup(t, bin)
	_ = BuildHash()
	replaceFileAt(t, bin, "release-2 bytes")

	h := newRunHarness(t)
	m, _ := newTestManager(t, nil, nil)
	go func() { h.done <- Run(context.Background(), upgradeRunOptions(h, h.signals, m)) }()
	<-h.ready
	h.signals <- UpgradeSignal

	waitRunReturn(t, h.done, "the upgrade signal")
	if calls.Load() != 1 {
		t.Fatalf("execFunc calls = %d; want 1 for a replaced startup binary", calls.Load())
	}
	if got := path.Load(); got != bin {
		t.Fatalf("exec path = %v; want the unchanged install path %q", got, bin)
	}
}

// TestUpgradeRejectsBinaryChangedDuringHandOff guards the second digest.
func TestUpgradeRejectsBinaryChangedDuringHandOff(t *testing.T) {
	for _, change := range []string{"replace", "rollback", "remove"} {
		t.Run(change, func(t *testing.T) {
			calls, _ := stubExec(t, nil)
			bin := writeTempBinary(t, "release-1 bytes")
			pinStartup(t, bin)
			_ = BuildHash()
			replaceFileAt(t, bin, "release-2 bytes")
			m, _ := newTestManager(t, nil, nil)
			m.BeforeRelaunch = func(context.Context) {
				switch change {
				case "replace":
					replaceFileAt(t, bin, "release-3 bytes")
				case "rollback":
					replaceFileAt(t, bin, "release-1 bytes")
				default:
					if err := os.Remove(bin); err != nil {
						t.Fatal(err)
					}
				}
			}
			ok, err := m.AttemptUpgrade(context.Background(), nil, nil, time.Second, nil, nil)
			want := cascade.New(cascade.KindUnavailable, "daemon: upgrade: installed binary changed during hand-off").Error()
			if ok || !cascade.HasKind(err, cascade.KindUnavailable) || err.Error() != want || calls.Load() != 0 || !m.Draining() {
				t.Fatalf("upgrade=%v err=%v calls=%d draining=%v", ok, err, calls.Load(), m.Draining())
			}
		})
	}
}

// TestUpgradeRejectsSymlinkChangedDuringHandOff guards the second target
// independently of the digest: both candidate targets have identical bytes.
func TestUpgradeRejectsSymlinkChangedDuringHandOff(t *testing.T) {
	calls, _ := stubExec(t, nil)
	bin := writeTempBinary(t, "release-1 bytes")
	link := filepath.Join(t.TempDir(), "installed")
	swapInstallLink(t, link, bin)
	pinStartup(t, link)
	_ = BuildHash()
	replaceFileAt(t, bin, "release-2 bytes")
	other := writeTempBinary(t, "release-2 bytes")
	m, _ := newTestManager(t, nil, nil)
	m.BeforeRelaunch = func(context.Context) { swapInstallLink(t, link, other) }
	ok, err := m.AttemptUpgrade(context.Background(), nil, nil, time.Second, nil, nil)
	if ok || !cascade.HasKind(err, cascade.KindUnavailable) || calls.Load() != 0 {
		t.Fatalf("upgrade=%v err=%v calls=%d; want false, unavailable, 0", ok, err, calls.Load())
	}
}

// TestUpgradeRetainsSymlinkedInstallPath simulates two successive launches
// through a stable installation symlink with a different release each time.
func TestUpgradeRetainsSymlinkedInstallPath(t *testing.T) {
	calls, path := stubExec(t, nil)
	link := filepath.Join(t.TempDir(), "installed")
	swapInstallLink(t, link, writeTempBinary(t, "release-1 bytes"))
	for i, contents := range []string{"release-2 bytes", "release-3 bytes"} {
		pinStartup(t, link)
		_ = BuildHash()
		swapInstallLink(t, link, writeTempBinary(t, contents))
		m, _ := newTestManager(t, nil, nil)
		ok, err := m.AttemptUpgrade(context.Background(), nil, nil, time.Second, nil, nil)
		if !ok || err != nil || calls.Load() != int32(i+1) || path.Load() != link {
			t.Fatalf("upgrade=%v err=%v calls=%d path=%v; want install path %q", ok, err, calls.Load(), path.Load(), link)
		}
	}
}

// TestRun_UpgradeRelaunchFails_FallsBackCleanly proves the non-bricking
// requirement at the Run level: when the exec of a replaced binary fails,
// Run still completes its normal drain-and-exit, and the pidfile/socket are
// cleaned up so a later `daemon start` finds a clean, recoverable state.
func TestRun_UpgradeRelaunchFails_FallsBackCleanly(t *testing.T) {
	calls, _ := stubExec(t, errors.New("exec format error"))
	pinStartupDigest(t, writeTempBinary(t, "skewed-and-unexecutable"), "1111111111111111111111111111111111111111111111111111111111111111")

	h := newRunHarness(t)
	m, _ := newTestManager(t, nil, nil)
	go func() { h.done <- Run(context.Background(), upgradeRunOptions(h, h.signals, m)) }()
	<-h.ready
	h.signals <- UpgradeSignal

	waitRunReturn(t, h.done, "a failed relaunch")
	if calls.Load() != 1 {
		t.Fatalf("execFunc calls = %d; want 1 (the relaunch must have been attempted)", calls.Load())
	}
	if _, err := os.Stat(h.pidPath); !os.IsNotExist(err) {
		t.Fatalf("pidfile still present after a failed relaunch fallback: %v", err)
	}
	if _, err := os.Stat(h.socketPath); !os.IsNotExist(err) {
		t.Fatalf("socket still present after a failed relaunch fallback: %v", err)
	}
}
