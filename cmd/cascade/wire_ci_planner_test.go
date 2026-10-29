//go:build !windows

package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/pkg/provider"
)

// plannerCheckpoint runs one checkpoint of a job whose mutable scope is the
// literal file footprint src/a.txt (the planner's form, jobs/planner.go),
// leased under repoID with its worktree cut from the rig repo, and returns
// the ci_dispatch outbox's total and unconfirmed rows.
func plannerCheckpoint(t *testing.T, repoID string) (total, open int) {
	t.Helper()
	r := newCIDaemonRig(t)
	r.setCILocal(t, "true", "lint", "build", "test")
	w, cancel, done := startTestDaemon(t, r.deps, observeWiring(t))
	defer func() { cancel(); awaitDaemonReturn(t, done) }()
	fr, ctx, tier := w.Deps.Fleet, context.Background(), provider.SensitivityInternal
	if err := fr.Jobs().PutJob(ctx, jobs.Job{ID: "job-plan", State: jobs.JobStatePending, CreatedAt: 1, UpdatedAt: 1,
		Capabilities: []string{"code"}, MutableScope: "src/a.txt", RiskClass: "low", MinTaskClass: "code",
		NodeRequirements: "{}", TimeoutSeconds: 60, CostCeiling: 1, Priority: 1,
		ConsequenceClass: jobs.ConsequenceNormal, DataClass: &tier}); err != nil {
		t.Fatalf("PutJob: %v", err)
	}
	if repoID == "" {
		repoID = r.repo
	}
	res, err := fr.Leases().Acquire(ctx, repoID, "src/a.txt", "job-plan")
	if err != nil || !res.Granted {
		t.Fatalf("Acquire = %+v, %v", res, err)
	}
	wt, err := fr.Worktrees().Create(ctx, res.Lease, r.repo)
	if err != nil || wt.Repo != r.repo {
		t.Fatalf("Create worktree = %+v, %v; want the row's Repo to be the repo root", wt, err)
	}
	writeCIFile(t, filepath.Join(wt.Path, "src", "a.txt"), "a\n")
	ciRigGit(t, wt.Path, "add", ".")
	ciRigGit(t, wt.Path, "commit", "-q", "-m", "planned change")
	r.tick(t)
	return awaitOutboxSettled(t, fr)
}

// TestDaemonPlannerFileScopeDispatches: a planner job with a literal file
// scope gets its checkpoint dispatched and every ci_dispatch row confirmed.
func TestDaemonPlannerFileScopeDispatches(t *testing.T) {
	if total, open := plannerCheckpoint(t, ""); total == 0 || open != 0 {
		t.Fatalf("ci_dispatch outbox = %d rows, %d unconfirmed; want the file-scoped checkpoint dispatched and confirmed", total, open)
	}
}

// TestDaemonCheckpointUsesWorktreeRepo: the lease's repo id is not a path,
// only the worktree row's Repo is; the checkpoint still dispatches, so the
// consumer reads the repo root from the worktree row, never lease.RepoID.
func TestDaemonCheckpointUsesWorktreeRepo(t *testing.T) {
	if total, open := plannerCheckpoint(t, "repo-id-not-a-path"); total == 0 || open != 0 {
		t.Fatalf("ci_dispatch outbox = %d rows, %d unconfirmed; want the checkpoint dispatched from the worktree row's repo root", total, open)
	}
}

// TestCIChangedPathsKeepsSpacedNames: a changed "Makefile copy" is one path,
// so it cannot pass as the literal scope "Makefile" (whitespace splitting
// would yield "Makefile" and "copy" and keep the literal).
func TestCIChangedPathsKeepsSpacedNames(t *testing.T) {
	repo := t.TempDir()
	ciRigGit(t, repo, "init", "-q", "-b", "main")
	writeCIFile(t, filepath.Join(repo, "Makefile"), "all:\n")
	ciRigGit(t, repo, "add", ".")
	ciRigGit(t, repo, "commit", "-q", "-m", "base")
	base := ciRigGit(t, repo, "rev-parse", "HEAD")
	writeCIFile(t, filepath.Join(repo, "Makefile copy"), "all:\n")
	ciRigGit(t, repo, "add", ".")
	ciRigGit(t, repo, "commit", "-q", "-m", "spaced")
	got, err := changedPaths(context.Background(), repo, base, ciRigGit(t, repo, "rev-parse", "HEAD"))
	if err != nil || len(got) != 1 || got[0] != "Makefile copy" {
		t.Fatalf("changedPaths = %q, %v; want exactly [\"Makefile copy\"]", got, err)
	}
	if kept := exactLiterals(got, []string{"Makefile"}); len(kept) != 0 {
		t.Fatalf("exactLiterals kept %q for a changed \"Makefile copy\"; want the literal dropped (fail closed)", kept)
	}
}
