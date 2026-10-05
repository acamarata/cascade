//go:build !windows

package daemon

// Purpose: UpgradeManager.BeforeRelaunch ordering (upgrade.go). The daemon
//   uses the hook to cancel its run context and join its supervised
//   goroutines before exec replaces the process image; this proves the hook
//   runs after Drain and before the exec stub, that the join really happens
//   before the stub runs, and that a nil hook keeps the old order.
// Constraints: syscall.Exec is stubbed through execFunc; no real process is
//   forked and no sleep is used (the join is bounded by a context).
// SPORT: internal/daemon (CHANGED, upgrade relaunch join).

import (
	"context"
	"log/slog"
	"reflect"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// upgradeOrder records the order of upgrade events across goroutines.
type upgradeOrder struct {
	mu     sync.Mutex
	events []string
}

func (o *upgradeOrder) add(e string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.events = append(o.events, e)
}

func (o *upgradeOrder) snapshot() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.events...)
}

// closerFunc adapts a func to io.Closer, standing in for the listener Drain
// closes.
type closerFunc func() error

func (f closerFunc) Close() error { return f() }

// skewedUpgrade pins a startup digest that never matches the temp binary it
// writes, stubs execFunc to record "exec", and returns the manager and the
// recorded order.
func skewedUpgrade(t *testing.T) (*UpgradeManager, *upgradeOrder) {
	t.Helper()
	pinStartupDigest(t, writeTempBinary(t, "skewed-binary-contents"), "2222222222222222222222222222222222222222222222222222222222222222")
	orig := execFunc
	t.Cleanup(func() { execFunc = orig })
	order := &upgradeOrder{}
	execFunc = func(string, []string, []string) error { order.add("exec"); return nil }
	m, _ := newTestManager(t, nil, nil)
	return m, order
}

// TestAttemptUpgradeJoinsBeforeRelaunch: BeforeRelaunch (the daemon's
// Manifest.RelaunchJoin hook) runs after Drain closed the listener and before
// the exec stub, and a supervised goroutine waiting on the run context has
// returned before the stub runs, within the drain grace.
func TestAttemptUpgradeJoinsBeforeRelaunch(t *testing.T) {
	m, order := skewedUpgrade(t)
	manifest := NewManifest(nil, nil)
	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()
	manifest.GoSupervised(runCtx, "test.consumer", "running", func(ctx context.Context) error {
		<-ctx.Done()
		order.add("goroutine-returned")
		return nil
	})
	const grace = 10 * time.Second
	join := manifest.RelaunchJoin(runCancel, grace)
	m.BeforeRelaunch = func(ctx context.Context) {
		order.add("before-relaunch")
		join(ctx)
		order.add("join-returned")
	}
	ln := closerFunc(func() error { order.add("drain-closed-listener"); return nil })

	relaunched, err := m.AttemptUpgrade(context.Background(), ln, nil, grace, nil, nil)
	if err != nil || !relaunched {
		t.Fatalf("AttemptUpgrade = %v, %v; want true, nil", relaunched, err)
	}
	want := []string{"drain-closed-listener", "before-relaunch", "goroutine-returned", "join-returned", "exec"}
	if got := order.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("upgrade order = %v, want %v", got, want)
	}
}

// TestAttemptUpgradeJoinsOnCancelledContext: the join completes even with a
// cancelled caller context, then the stop aborts exec. The join's grace
// bound must not inherit that cancel: supervised work still finishes.
func TestAttemptUpgradeJoinsOnCancelledContext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m, order := skewedUpgrade(t)
		manifest := NewManifest(nil, nil)
		runCtx, runCancel := context.WithCancel(context.Background())
		gate, done := make(chan struct{}), make(chan struct{})
		manifest.GoSupervised(runCtx, "test.gated", "gated", func(ctx context.Context) error {
			<-ctx.Done()
			<-gate
			order.add("goroutine-returned")
			return nil
		})
		m.BeforeRelaunch = manifest.RelaunchJoin(runCancel, 10*time.Second)
		callerCtx, cancelCaller := context.WithCancel(context.Background())
		cancelCaller()
		go func() {
			defer close(done)
			ok, err := m.AttemptUpgrade(callerCtx, nil, nil, time.Second, nil, nil)
			want := cascade.Wrap(cascade.KindUnavailable, context.Canceled, "daemon: upgrade: aborted by stop")
			if ok || !cascade.HasKind(err, cascade.KindUnavailable) || err.Error() != want.Error() {
				t.Errorf("AttemptUpgrade = %v, %v; want false, %v", ok, err, want)
			}
		}()
		synctest.Wait()
		select {
		case <-done:
			t.Error("AttemptUpgrade returned while a supervised goroutine still ran: the join ended with the cancelled caller ctx")
		default:
		}
		close(gate)
		<-done
		manifest.Wait()
		if got, want := order.snapshot(), []string{"goroutine-returned"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("upgrade order = %v, want %v", got, want)
		}
	})
}

// TestAttemptUpgradeNilBeforeRelaunchKeepsOrder: with no hook the old
// drain-then-exec order is unchanged.
func TestAttemptUpgradeNilBeforeRelaunchKeepsOrder(t *testing.T) {
	m, order := skewedUpgrade(t)
	ln := closerFunc(func() error { order.add("drain-closed-listener"); return nil })
	relaunched, err := m.AttemptUpgrade(context.Background(), ln, nil, time.Second, nil, nil)
	if err != nil || !relaunched {
		t.Fatalf("AttemptUpgrade = %v, %v; want true, nil", relaunched, err)
	}
	want := []string{"drain-closed-listener", "exec"}
	if got := order.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("upgrade order = %v, want %v", got, want)
	}
}

// TestRelaunchJoinGivesUpAfterGrace: a supervised goroutine that ignores the
// cancelled run context cannot block the relaunch. The hook returns once the
// grace bound ends and logs one WARN line naming the timeout.
func TestRelaunchJoinGivesUpAfterGrace(t *testing.T) {
	logs := &lockedBuffer{}
	manifest := NewManifest(slog.New(slog.NewJSONHandler(logs, nil)), nil)
	release := make(chan struct{})
	manifest.GoSupervised(context.Background(), "test.stuck", "ignoring cancel", func(context.Context) error {
		<-release
		return nil
	})
	cancelled := false
	manifest.RelaunchJoin(func() { cancelled = true }, 0)(context.Background())
	close(release)
	manifest.Wait()
	if !cancelled {
		t.Fatal("RelaunchJoin did not cancel the run context")
	}
	var warns int
	for _, rec := range logs.lines(t) {
		if rec["level"] == "WARN" && rec["subsystem"] == relaunchJoinName {
			warns++
		}
	}
	if warns != 1 {
		t.Fatalf("RelaunchJoin logged %d WARN lines for an expired join, want 1", warns)
	}
}
