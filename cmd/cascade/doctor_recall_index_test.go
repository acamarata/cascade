// Purpose: proves the retrieval_index doctor check is mounted on the
// REAL productionCheckRegistry (not a test-built registry), so a check
// that exists, compiles, and passes its own package tests but is never
// registered here reads as absent from `cascade doctor` — the exact
// pattern doctor.go's own comment names as the reason only checks with a
// real data source are mounted.
//
// SPORT: cmd/cascade/doctor (CHANGED, retrieval_index check, P1-E06-W2-S11-T4).
package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/doctor"
	"github.com/acamarata/cascade/internal/retrieval/lifecycle"
	"github.com/acamarata/cascade/providers/sqlite"
)

// TestDoctorRetrievalIndexCheckIsRegistered proves productionCheckRegistry
// (the exact function newDoctorCmd's production path calls) mounts the
// retrieval_index check. Deleting the reg.Register call for it from
// doctor.go turns this red.
func TestDoctorRetrievalIndexCheckIsRegistered(t *testing.T) {
	useTempCustody(t)
	reg, err := productionCheckRegistry(context.Background(), doctorTestPaths(t), doctorTestClock())
	if err != nil {
		t.Fatalf("productionCheckRegistry: %v", err)
	}
	for _, check := range reg.List() {
		if check.Name() == lifecycle.DoctorCheckName {
			return
		}
	}
	t.Fatalf("retrieval_index is not registered on the real productionCheckRegistry")
}

// TestBuildRecallIndexManager_LiveDaemonLock reproduces
// DEFECT-doctor-retrieval-index-exclusive-lock.md end to end: a second
// real sqlite.Open against paths.DataDir()/cascade.db, held open exactly
// as a running `cascade daemon` would hold it (the same setup
// internal/runtime's TestWriteArbitration uses to prove the arbitration
// primitive itself), then the retrieval_index doctor check run through it.
//
// Before the fix this failed with exit-5-shaped StatusError ("could not
// open the retrieval index" / "sqlite: exclusive lock held by another
// process") on every run against a live daemon — the gate's own
// reproduction. After the fix, Run must report a non-error status: the
// check has nothing built yet in this fresh temp dir, so the honest
// answer is StatusOK "no retrieval index has been built yet", reached
// through the read-only fallback rather than through failure.
func TestBuildRecallIndexManager_LiveDaemonLock(t *testing.T) {
	paths := doctorTestPaths(t)
	if err := os.MkdirAll(paths.DataDir(), 0o700); err != nil {
		t.Fatalf("MkdirAll(DataDir): %v", err)
	}
	dbPath := filepath.Join(paths.DataDir(), "cascade.db")
	ctx := context.Background()

	daemonDriver, err := sqlite.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("simulate the daemon's own Open: %v", err)
	}
	t.Cleanup(func() { _ = daemonDriver.Close() })

	check := lifecycle.NewDoctorCheck(buildRecallIndexManager(paths, doctorTestClock()))
	result, err := check.Run(ctx)
	if err != nil {
		t.Fatalf("DoctorCheck.Run against a live daemon lock returned an error: %v, want (result, nil)", err)
	}
	if result.Status == doctor.StatusError {
		t.Fatalf("DoctorCheck.Run against a live daemon lock = %+v, want a non-error status "+
			"(a flock conflict must never be a read failure)", result)
	}
	if result.Message != "no retrieval index has been built yet" {
		t.Fatalf("DoctorCheck.Run against a live daemon lock: Message = %q, want the honest "+
			"no-index-yet message (read-only fallback must still see the real filesystem state)", result.Message)
	}
}

// TestDoctorGitTreeHash_MatchesRealGit drives doctorGitTreeHash against
// this repo's own working tree and asserts its "HEAD:status-digest" shape
// against an independently-run `git rev-parse HEAD` — the same real
// counterpart the function itself shells out to (not a second copy of the
// function's own logic).
func TestDoctorGitTreeHash_MatchesRealGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	want, err := exec.CommandContext(context.Background(), "git", "rev-parse", "HEAD").Output()
	if err != nil {
		t.Skipf("not inside a git worktree: %v", err)
	}
	got, err := doctorGitTreeHash(context.Background())
	if err != nil {
		t.Fatalf("doctorGitTreeHash: %v", err)
	}
	wantHead := strings.TrimSpace(string(want))
	parts := strings.SplitN(got, ":", 2)
	if len(parts) != 2 {
		t.Fatalf("doctorGitTreeHash() = %q, want \"<head>:<digest>\"", got)
	}
	if parts[0] != wantHead {
		t.Fatalf("doctorGitTreeHash() head = %q, want %q (real git rev-parse HEAD)", parts[0], wantHead)
	}
	if parts[1] == "" {
		t.Fatalf("doctorGitTreeHash() status digest is empty, want a chunk id (even for a clean tree)")
	}
}
