package daemon

// Purpose: deterministic coverage for RegisterWorktreeManager's three
//
//	error/default branches (subsystems_worktree.go) that
//	subsystems_test.go's happy-path acquired/create test never drives:
//	the nil-resolveRoot default, bus.Subscribe's KindConflict refusal,
//	and the background goroutine's Run-error -> Failed path. Split from
//	subsystems_test.go to keep it under the 300-line cap (Art.10.3),
//	same rationale as subsystems_worktree.go's own split from
//	subsystems.go.
//
// CONTRACT NOTE: the goroutine-error case was previously exercised only
// incidentally, by a race between the background goroutine and the
// happy-path test's own teardown; that race resolved one way often
// enough on darwin to cover the line and the other way on Linux CI,
// which is why the coverage gate flipped between platforms for the exact
// same commit. This file drives that branch on purpose, with a payload
// guaranteed to fail decode, so the coverage no longer depends on
// goroutine scheduling.
//
// P1-CORE-06 adds the fence requirement: RegisterWorktreeSweep refuses a
// nil fence, and an acquired event for a released lease reaches the real
// fenced Create through the composition root and creates nothing.
//
// SPORT: internal/daemon (ADD, P1-E29-W6-S59-T3 coverage follow-up).

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
)

// daemonLeaseFence returns the real (*jobs.LeaseManager).Fence over store,
// the fence production wiring passes.
func daemonLeaseFence(store *jobs.Store) jobs.FenceFunc {
	return jobs.NewLeaseManager(store, runtime.NewSystemClock(), func() bool { return true }, jobs.DefaultLeaseDefaults(), nil).Fence
}

// permissiveFixtureFence never refuses. Only fixture setup uses it, to
// build a worktree under a lease the test then treats as an orphan.
func permissiveFixtureFence(context.Context, string, string, int64) error { return nil }

// newDaemonWorktreeManager constructs a jobs.WorktreeManager with fence,
// failing the test on a constructor error.
func newDaemonWorktreeManager(t *testing.T, store *jobs.Store, fence jobs.FenceFunc) *jobs.WorktreeManager {
	t.Helper()
	wt, err := jobs.NewWorktreeManager(store, nil, nil, fakeProbe{alive: false}, fence)
	if err != nil {
		t.Fatalf("NewWorktreeManager: %v", err)
	}
	return wt
}

// TestRegisterWorktreeSweepRequiresFence proves the composition-root call
// cannot build an unfenced manager: a nil fence returns KindInvalidInput,
// no manager, and records the sweep subsystem as failed.
func TestRegisterWorktreeSweepRequiresFence(t *testing.T) {
	store := newDaemonTestJobsStore(t)
	m := NewManifest(nil, runtime.NewSystemClock())
	wt, _, err := m.RegisterWorktreeSweep(context.Background(), store, nil, nil, fakeProbe{alive: false}, nil)
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
		t.Fatalf("RegisterWorktreeSweep(nil fence) err = %v, want KindInvalidInput", err)
	}
	if wt != nil {
		t.Fatal("RegisterWorktreeSweep(nil fence) returned a manager")
	}
	for _, s := range m.Snapshot() {
		if s.Name == worktreeSweepSubsystem && s.State != SubsystemError {
			t.Fatalf("sweep subsystem state = %v, want SubsystemError", s.State)
		}
	}
}

// TestDaemonSubsystems_WorktreeManagerFencedAcquireCreatesNothing drives
// an acquired event for a RELEASED lease through RegisterWorktreeManager:
// the real fenced Create refuses, so no worktree directory and no row
// exist afterwards, and the refusal surfaces as the subsystem's error.
func TestDaemonSubsystems_WorktreeManagerFencedAcquireCreatesNothing(t *testing.T) {
	store := newDaemonTestJobsStore(t)
	repo := newDaemonTestGitRepo(t)
	bus := events.New(storetest.NewMemStore(), runtime.NewSystemClock())
	wt := newDaemonWorktreeManager(t, store, daemonLeaseFence(store))
	lease := jobs.ResourceLease{RepoID: repo, ScopeGlob: "**", Holder: "job-fenced", Epoch: 1, State: jobs.LeaseReleased}
	ctx := context.Background()
	if err := store.PutLease(ctx, lease); err != nil {
		t.Fatalf("PutLease: %v", err)
	}
	runCtx, cancel := context.WithCancel(context.Background())
	m := NewManifest(nil, runtime.NewSystemClock())
	t.Cleanup(func() {
		cancel()
		m.Wait()
	})
	if err := m.RegisterWorktreeManager(runCtx, bus, wt, nil, "fenced-cursor"); err != nil {
		t.Fatalf("RegisterWorktreeManager: %v", err)
	}
	publishAcquired(ctx, t, bus, lease)
	assertSubsystemState(t, m, SubsystemError)

	path := filepath.Join(repo, ".cascade", "worktrees", "job-"+lease.Holder)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("fenced acquire created %q (stat err=%v)", path, err)
	}
	if _, ok, err := store.GetWorktree(ctx, path); err != nil || ok {
		t.Fatalf("fenced acquire left a worktree row: ok=%v err=%v", ok, err)
	}
}

// TestDaemonSubsystems_WorktreeManagerNilResolveRootDefaultsToIdentity
// proves the nil-resolveRoot branch actually installs
// jobs.IdentityRepoRootResolver: a nil resolver plus a real acquired
// event against a real git repo still produces a real worktree, exactly
// as the explicit-resolver test does, because RepoID IS the repo root.
func TestDaemonSubsystems_WorktreeManagerNilResolveRootDefaultsToIdentity(t *testing.T) {
	store := newDaemonTestJobsStore(t)
	repo := newDaemonTestGitRepo(t)
	bus := events.New(storetest.NewMemStore(), runtime.NewSystemClock())
	wt := newDaemonWorktreeManager(t, store, daemonLeaseFence(store))

	lease := jobs.ResourceLease{RepoID: repo, ScopeGlob: "**", Holder: "job-nil-resolver", Epoch: 1, State: jobs.LeaseHeld}
	ctx := context.Background()
	if err := store.PutLease(ctx, lease); err != nil {
		t.Fatalf("PutLease: %v", err)
	}

	runCtx, cancel := context.WithCancel(context.Background())
	m := NewManifest(nil, runtime.NewSystemClock())
	// Cancel AND join before the test's TempDir is removed. t.Cleanup runs
	// LIFO, so this registers after newDaemonTestGitRepo's TempDir and
	// therefore runs before its RemoveAll. Cancelling alone is not enough:
	// the manager goroutine can still be mid-git-operation, and on Windows
	// an open handle makes removing the worktree directory fail outright.
	t.Cleanup(func() {
		cancel()
		m.Wait()
	})
	if err := m.RegisterWorktreeManager(runCtx, bus, wt, nil, "nil-resolver-cursor"); err != nil {
		t.Fatalf("RegisterWorktreeManager with nil resolveRoot: %v", err)
	}

	publishAcquired(ctx, t, bus, lease)
	wantPath := filepath.Join(repo, ".cascade", "worktrees", "job-"+lease.Holder)
	waitForRunningWithPathCreated(t, m, wantPath)
}

// TestDaemonSubsystems_WorktreeManagerSubscribeConflictReportsFailed
// proves the bus.Subscribe error branch: a cursor already active on the
// same namespace makes Subscribe return cascade.KindConflict, which
// RegisterWorktreeManager must both return to its caller and record as
// SubsystemError, never silently swallow.
func TestDaemonSubsystems_WorktreeManagerSubscribeConflictReportsFailed(t *testing.T) {
	store := newDaemonTestJobsStore(t)
	bus := events.New(storetest.NewMemStore(), runtime.NewSystemClock())
	wt := newDaemonWorktreeManager(t, store, daemonLeaseFence(store))

	ctx := context.Background()
	held, err := bus.Subscribe(ctx, jobs.LeaseEventNamespace, "conflict-cursor", 1)
	if err != nil {
		t.Fatalf("priming Subscribe: %v", err)
	}
	t.Cleanup(func() { _ = held.Unsubscribe() })

	m := NewManifest(nil, runtime.NewSystemClock())
	if err := m.RegisterWorktreeManager(ctx, bus, wt, nil, "conflict-cursor"); err == nil {
		t.Fatal("RegisterWorktreeManager over an already-active cursor: want a KindConflict error, got nil")
	}

	assertSubsystemState(t, m, SubsystemError)
}

// TestDaemonSubsystems_WorktreeManagerRunErrorReportsFailed proves the
// background goroutine's error path deterministically: a published event
// with a payload that cannot decode as JSON makes apply (and therefore
// Run) return an error, which the goroutine must record as
// SubsystemError. Polls rather than sleeping a fixed duration, since the
// goroutine's error still lands asynchronously.
func TestDaemonSubsystems_WorktreeManagerRunErrorReportsFailed(t *testing.T) {
	store := newDaemonTestJobsStore(t)
	bus := events.New(storetest.NewMemStore(), runtime.NewSystemClock())
	wt := newDaemonWorktreeManager(t, store, daemonLeaseFence(store))

	runCtx, cancel := context.WithCancel(context.Background())
	m := NewManifest(nil, runtime.NewSystemClock())
	// Join the goroutine rather than only cancelling it, so it cannot
	// outlive the test and report into a Manifest a later test is reading.
	t.Cleanup(func() {
		cancel()
		m.Wait()
	})
	if err := m.RegisterWorktreeManager(runCtx, bus, wt, nil, "bad-payload-cursor"); err != nil {
		t.Fatalf("RegisterWorktreeManager: %v", err)
	}

	if _, err := bus.Publish(context.Background(), jobs.LeaseEventNamespace, jobs.EventLeaseAcquired, "test", []byte("not valid json")); err != nil {
		t.Fatalf("Publish malformed payload: %v", err)
	}

	assertSubsystemState(t, m, SubsystemError)
}

// publishAcquired marshals and publishes a real EventLeaseAcquired for
// lease, split out so both nil- and explicit-resolveRoot tests share one
// payload shape (must match leasePayload's wire fields exactly).
func publishAcquired(ctx context.Context, t *testing.T, bus *events.Bus, lease jobs.ResourceLease) {
	t.Helper()
	payload := map[string]any{
		"repo_id": lease.RepoID, "scope_glob": lease.ScopeGlob, "holder": lease.Holder,
		"epoch": lease.Epoch, "state": "held",
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if _, err := bus.Publish(ctx, jobs.LeaseEventNamespace, jobs.EventLeaseAcquired, "test", raw); err != nil {
		t.Fatalf("Publish: %v", err)
	}
}

// waitForRunningWithPathCreated polls the filesystem for wantPath and the
// Manifest for SubsystemRunning, up to 5s -- the same bound and pattern
// TestDaemonSubsystems_WorktreeManagerAcquiredWiresRealCreate uses.
func waitForRunningWithPathCreated(t *testing.T, m *Manifest, wantPath string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, statErr := os.Stat(wantPath); statErr == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("worktree at %q never appeared: nil resolveRoot did not wire through to a real Create", wantPath)
		}
		time.Sleep(20 * time.Millisecond)
	}
	assertSubsystemState(t, m, SubsystemRunning)
}

// assertSubsystemState polls m's Snapshot for worktreeManagerSubsystem to
// reach want, up to 5s, since the goroutine's state transition (Running
// at Register time; a later Error from Run's own failure) is always
// asynchronous relative to the calling test.
func assertSubsystemState(t *testing.T, m *Manifest, want SubsystemState) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		for _, s := range m.Snapshot() {
			if s.Name == worktreeManagerSubsystem && s.State == want {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("worktree manager subsystem never reached state %v: snapshot=%+v", want, m.Snapshot())
		}
		time.Sleep(20 * time.Millisecond)
	}
}
