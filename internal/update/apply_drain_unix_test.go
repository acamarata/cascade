//go:build !windows

package update

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestApplyDrainHandoff is acceptance [2]: Swap, then exactly one
// SIGUSR2 (never SIGTERM), then WaitVersion sees the candidate.
func TestApplyDrainHandoff(t *testing.T) {
	f := newFixture(t, "2.0.0", "2.0.1")
	d := f.deps("2.0.0", nil)
	var events []string
	var sigs []syscall.Signal
	d.Daemon.Status = func(context.Context) (bool, int, error) { return true, 4242, nil }
	d.Daemon.Signal = UpgradeSignaller(func(pid int, sig syscall.Signal) error {
		sigs = append(sigs, sig)
		if pid != 4242 {
			t.Errorf("signalled pid %d, want 4242", pid)
		}
		events = append(events, "signal swapped="+f.read("cascade"))
		return nil
	})
	d.Daemon.Version = func(context.Context) (string, error) {
		events = append(events, "version")
		if len(sigs) == 0 {
			return "2.0.0", nil
		}
		return "2.0.1", nil
	}
	d.Daemon.Poll, d.Daemon.Timeout = time.Millisecond, time.Second
	res, err := Apply(context.Background(), d)
	must(t, err)
	if len(sigs) != 1 || sigs[0] != syscall.SIGUSR2 || sigs[0] == syscall.SIGTERM {
		t.Fatalf("signals = %v, want exactly one SIGUSR2", sigs)
	}
	if len(events) != 2 || events[0] != "signal swapped=cascade 2.0.1" || events[1] != "version" {
		t.Fatalf("events = %v, want the swap before the one signal, then the version poll", events)
	}
	if !res.Updated || !res.DaemonSignalled {
		t.Fatalf("result = %+v", res)
	}
}

// TestApplyHandoffTimeoutRestoresPrevious is acceptance [2]: a daemon that
// never reports the candidate leaves the previous files restored and a
// KindUnavailable naming `cascade daemon start`.
func TestApplyHandoffTimeoutRestoresPrevious(t *testing.T) {
	f := newFixture(t, "2.0.0", "2.0.1")
	before := map[string]string{}
	for _, n := range executableBases {
		before[n] = f.read(n)
	}
	receiptBefore, err := os.ReadFile(f.receipt)
	must(t, err)
	d := f.deps("2.0.0", nil)
	signals := 0
	d.Daemon.Status = func(context.Context) (bool, int, error) { return true, 7, nil }
	d.Daemon.Signal = UpgradeSignaller(func(int, syscall.Signal) error { signals++; return nil })
	d.Daemon.Version = func(context.Context) (string, error) {
		f.clock.Advance(time.Second)
		return "2.0.0", nil
	}
	d.Daemon.Poll, d.Daemon.Timeout = time.Millisecond, 30*time.Second
	_, err = Apply(context.Background(), d)
	wantKind(t, err, cascade.KindUnavailable, "`cascade daemon start`")
	if signals != 1 {
		t.Fatalf("signals = %d, want 1", signals)
	}
	for n, want := range before {
		if got := f.read(n); got != want {
			t.Fatalf("%s = %q after the timeout, want the restored %q", n, got, want)
		}
	}
	if b, err := os.ReadFile(f.receipt); err != nil || string(b) != string(receiptBefore) {
		t.Fatalf("receipt changed after a failed hand-off: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(PreviousDir(f.dataDir), "cascade")); err != nil || string(b) != "cascade 2.0.0" {
		t.Fatalf("previous dir lost the previous cascade: %q, %v", b, err)
	}
}

// TestDaemonStatusReadsPidfile covers the production status probe.
func TestDaemonStatusReadsPidfile(t *testing.T) {
	running, pid, err := DaemonStatus(filepath.Join(t.TempDir(), "daemon.pid"), newFixture(t, "2.0.0", "2.0.1").clock)(context.Background())
	if err != nil || running || pid != 0 {
		t.Fatalf("status with no pidfile = %v %d %v", running, pid, err)
	}
	if ProductionSignal() == nil {
		t.Fatal("no production signal sender")
	}
}

// TestApplyFailureAfterSignalNamesRestart: once the hand-off signal was
// sent, a later failure (the version wait, or the receipt rewrite) restores
// the previous files, says the daemon may already run the new binary and
// names `cascade daemon restart`; no second signal is sent.
func TestApplyFailureAfterSignalNamesRestart(t *testing.T) {
	for _, receiptFails := range []bool{false, true} {
		f := newFixture(t, "2.0.0", "2.0.1")
		d := f.deps("2.0.0", nil)
		signals := 0
		d.Daemon.Status = func(context.Context) (bool, int, error) { return true, 7, nil }
		d.Daemon.Signal = UpgradeSignaller(func(int, syscall.Signal) error { signals++; return nil })
		receiptDir := filepath.Dir(f.receipt)
		t.Cleanup(func() { _ = os.Chmod(receiptDir, 0o755) })
		d.Daemon.Version = func(context.Context) (string, error) {
			if receiptFails {
				must(t, os.Chmod(receiptDir, 0o500))
				return "2.0.1", nil
			}
			f.clock.Advance(time.Second)
			return "2.0.0", nil
		}
		d.Daemon.Poll, d.Daemon.Timeout = time.Millisecond, 3*time.Second
		_, err := Apply(context.Background(), d)
		if err == nil {
			t.Fatalf("receiptFails=%v: apply succeeded", receiptFails)
		}
		for _, want := range []string{"may run the new binary", "`cascade daemon restart`", "the previous version was restored"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("receiptFails=%v: error %q does not say %q", receiptFails, err, want)
			}
		}
		if signals != 1 || f.read("cascade") != "cascade 2.0.0" {
			t.Fatalf("receiptFails=%v: %d signals, cascade = %q; want one signal and the restored file", receiptFails, signals, f.read("cascade"))
		}
	}
}
