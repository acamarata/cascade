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
	"testing"

	"github.com/acamarata/cascade/internal/daemon"
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

// TestDoctorAndDaemonAgreeOnADirtyTree is the assertion this check
// shipped without: that the marker `cascade doctor` computes and the
// marker the daemon's recall.index.* handlers compute are the SAME value
// on a working tree with an uncommitted change.
//
// The doctor used to carry its own copy of the algorithm, under a comment
// asserting it was identical to the daemon's. It hashed `git status
// --porcelain` WITHOUT trimming the trailing newline the daemon's helper
// strips, so every dirty tree produced two different digests: `cascade
// recall index verify` reported the marker current, `cascade doctor`
// reported it drifted, and doctor exited 5 forever on any machine with an
// uncommitted edit (R-14.278).
//
// A CLEAN tree could not catch it — both halves hash the empty string to
// the same digest — which is why the two shape-only tests that preceded
// this one both passed. So this test dirties the tree on purpose.
func TestDoctorAndDaemonAgreeOnADirtyTree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	seedGitRepo(t, dir)
	chdirForTest(t, dir)

	clean, err := daemon.GitTreeHash(context.Background())
	if err != nil {
		t.Fatalf("GitTreeHash (clean): %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("edited\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dirty, err := daemon.GitTreeHash(context.Background())
	if err != nil {
		t.Fatalf("GitTreeHash (dirty): %v", err)
	}
	if dirty == clean {
		t.Fatal("the marker did not move after an uncommitted edit; drift would be invisible until commit")
	}

	// What the doctor check itself is wired to, reached through the real
	// builder rather than by naming the function again — a test that
	// called daemon.GitTreeHash twice would pass even if the check were
	// re-pointed at a second copy tomorrow.
	viaDoctor, err := doctorMarkerFunc()(context.Background())
	if err != nil {
		t.Fatalf("the doctor check's tree-hash func: %v", err)
	}
	if viaDoctor != dirty {
		t.Fatalf("doctor computes %q and the daemon computes %q for the same dirty tree; "+
			"`doctor` and `recall index verify` would disagree out loud", viaDoctor, dirty)
	}
}

// seedGitRepo makes a one-commit repository in dir.
func seedGitRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init"}, {"config", "user.email", "t@example.invalid"}, {"config", "user.name", "t"},
		{"add", "."}, {"commit", "-m", "seed"},
	} {
		cmd := exec.CommandContext(context.Background(), "git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

// chdirForTest enters dir and restores the old working directory.
func chdirForTest(t *testing.T, dir string) {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
}
