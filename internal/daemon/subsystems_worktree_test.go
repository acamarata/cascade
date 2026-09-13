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
)

// TestDaemonSubsystems_WorktreeManagerNilResolveRootDefaultsToIdentity
// proves the nil-resolveRoot branch actually installs
// jobs.IdentityRepoRootResolver: a nil resolver plus a real acquired
// event against a real git repo still produces a real worktree, exactly
// as the explicit-resolver test does, because RepoID IS the repo root.
func TestDaemonSubsystems_WorktreeManagerNilResolveRootDefaultsToIdentity(t *testing.T) {
	store := newDaemonTestJobsStore(t)
	repo := newDaemonTestGitRepo(t)
	bus := events.New(storetest.NewMemStore(), runtime.NewSystemClock())
	wt := jobs.NewWorktreeManager(store, nil, nil, fakeProbe{alive: false})

	lease := jobs.ResourceLease{RepoID: repo, ScopeGlob: "**", Holder: "job-nil-resolver", Epoch: 1, State: jobs.LeaseHeld}
	ctx := context.Background()
	if err := store.PutLease(ctx, lease); err != nil {
		t.Fatalf("PutLease: %v", err)
	}

	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := NewManifest(nil, runtime.NewSystemClock())
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
	wt := jobs.NewWorktreeManager(store, nil, nil, fakeProbe{alive: false})

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
	wt := jobs.NewWorktreeManager(store, nil, nil, fakeProbe{alive: false})

	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := NewManifest(nil, runtime.NewSystemClock())
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
