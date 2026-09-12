package daemon

// Purpose: drives RegisterWorktreeSweep and RegisterWorktreeManager
//
//	(subsystems.go, subsystems_worktree.go) through their REAL entry
//	points against a real jobs.Store + real git repository, proving the
//	AC/S-59.T3 orphan sweep and acquired/released event wiring actually
//	reach production behavior at this composition root — the same
//	precedent subsystems_router_test.go already sets for
//	RegisterConductorRouter.
//
// SPORT: internal/daemon (ADD, P1-E29-W6-S59-T3).

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver for this file's real store

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/internal/storage/storetest"
)

// fakeProbe is a deterministic jobs.ProcessLivenessProbe for this file's
// tests, mirroring internal/jobs' own fakeLivenessProbe precedent
// (unexported there, so this package defines its own).
type fakeProbe struct{ alive bool }

func (p fakeProbe) IsAlive(int64) bool { return p.alive }

func runGitDaemonTest(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v (in %s): %v\n%s", args, dir, err, out)
	}
}

// newDaemonTestGitRepo mirrors internal/jobs/worktree_test.go's
// newTestGitRepo exactly (real git init + commit, symlink-resolved so its
// path is byte-identical to what `git worktree list` itself reports).
func newDaemonTestGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	runGitDaemonTest(t, dir, "init", "-q")
	runGitDaemonTest(t, dir, "config", "user.email", "daemon-worktree-test@example.invalid")
	runGitDaemonTest(t, dir, "config", "user.name", "daemon-worktree-test")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	runGitDaemonTest(t, dir, "add", "README.md")
	runGitDaemonTest(t, dir, "commit", "-q", "-m", "init")
	return dir
}

// newDaemonTestJobsStore opens a real modernc-sqlite file under
// t.TempDir() and applies the real jobs schema — Art.2, never an
// in-memory double.
func newDaemonTestJobsStore(t *testing.T) *jobs.Store {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	clock := runtime.NewSystemClock()
	if err := jobs.ApplyJobsSchema(context.Background(), db, migrate.SQLiteEmitter{}, clock, "", ""); err != nil {
		t.Fatalf("ApplyJobsSchema: %v", err)
	}
	return jobs.NewStore(db)
}

// TestDaemonSubsystems_WorktreeSweepWiredAtStartup proves
// RegisterWorktreeSweep actually runs jobs.WorktreeManager.Sweep against
// real git and a real store: a released, clean, dead-pgid orphan created
// BEFORE Register is called is gone from disk and from the store
// afterward, and the Manifest records the subsystem Running.
func TestDaemonSubsystems_WorktreeSweepWiredAtStartup(t *testing.T) {
	store := newDaemonTestJobsStore(t)
	repo := newDaemonTestGitRepo(t)
	ctx := context.Background()
	fixtureWt := jobs.NewWorktreeManager(store, nil, nil, fakeProbe{alive: false})

	lease := jobs.ResourceLease{RepoID: repo, ScopeGlob: "**", Holder: "job-sweep", Epoch: 1, State: jobs.LeaseReleased}
	if err := store.PutLease(ctx, lease); err != nil {
		t.Fatalf("PutLease: %v", err)
	}
	w, err := fixtureWt.Create(ctx, lease, repo)
	if err != nil {
		t.Fatalf("Create fixture worktree: %v", err)
	}

	m := NewManifest(nil, runtime.NewSystemClock())
	_, result, err := m.RegisterWorktreeSweep(ctx, store, nil, nil, fakeProbe{alive: false})
	if err != nil {
		t.Fatalf("RegisterWorktreeSweep: %v", err)
	}
	if len(result.Removed) != 1 || result.Removed[0] != w.Path {
		t.Fatalf("Sweep via RegisterWorktreeSweep removed = %v, want [%q]", result.Removed, w.Path)
	}
	if _, statErr := os.Stat(w.Path); !os.IsNotExist(statErr) {
		t.Fatalf("orphan worktree still present after RegisterWorktreeSweep: err=%v", statErr)
	}

	snap := m.Snapshot()
	found := false
	for _, s := range snap {
		if s.Name == worktreeSweepSubsystem {
			found = true
			if s.State != SubsystemRunning {
				t.Fatalf("worktree sweep subsystem state = %v, want Running", s.State)
			}
		}
	}
	if !found {
		t.Fatalf("Manifest snapshot has no %q entry: %+v", worktreeSweepSubsystem, snap)
	}
}

// TestDaemonSubsystems_WorktreeManagerAcquiredWiresRealCreate proves
// RegisterWorktreeManager's background loop actually consumes a REAL
// bus-published acquired event and drives a REAL `git worktree add`: no
// CLI verb or RPC method exists for this (S-60.T1's scope), so this
// composition-root call is the only production path that can prove it.
func TestDaemonSubsystems_WorktreeManagerAcquiredWiresRealCreate(t *testing.T) {
	store := newDaemonTestJobsStore(t)
	repo := newDaemonTestGitRepo(t)
	bus := events.New(storetest.NewMemStore(), runtime.NewSystemClock())
	wt := jobs.NewWorktreeManager(store, nil, nil, fakeProbe{alive: false})

	lease := jobs.ResourceLease{RepoID: repo, ScopeGlob: "**", Holder: "job-evt", Epoch: 1, State: jobs.LeaseHeld}
	ctx := context.Background()
	if err := store.PutLease(ctx, lease); err != nil {
		t.Fatalf("PutLease: %v", err)
	}

	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := NewManifest(nil, runtime.NewSystemClock())
	if err := m.RegisterWorktreeManager(runCtx, bus, wt, jobs.IdentityRepoRootResolver, "test-cursor"); err != nil {
		t.Fatalf("RegisterWorktreeManager: %v", err)
	}

	payload, err := json.Marshal(map[string]any{
		"repo_id": repo, "scope_glob": "**", "holder": "job-evt", "epoch": int64(1), "state": "held",
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if _, err := bus.Publish(ctx, jobs.LeaseEventNamespace, jobs.EventLeaseAcquired, "test", payload); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	wantPath := filepath.Join(repo, ".cascade", "worktrees", "job-job-evt")
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, statErr := os.Stat(wantPath); statErr == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("worktree at %q never appeared: RegisterWorktreeManager's event wiring did not drive a real Create", wantPath)
		}
		time.Sleep(20 * time.Millisecond)
	}

	snap := m.Snapshot()
	for _, s := range snap {
		if s.Name == worktreeManagerSubsystem && s.State != SubsystemRunning {
			t.Fatalf("worktree manager subsystem state = %v, want Running", s.State)
		}
	}
}
