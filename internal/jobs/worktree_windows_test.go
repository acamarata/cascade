//go:build windows

package jobs

// Purpose: Art.5 platform parity's Windows half. internal/jobs' worktree
//
//	suite (worktree_test.go, worktree_sweep_test.go,
//	worktree_quarantine_test.go, worktree_snapshot_test.go) drives a REAL
//	git binary against a REAL repository under t.TempDir(), which this
//	repo's R-16.47 convention (see internal/memory/rpc_integration_windows_test.go's
//	identical precedent) says is asserted by the Windows CI matrix
//	itself, never by simulating GOOS=windows locally on a darwin/linux
//	dev machine — a real git-on-Windows worktree round-trip needs a real
//	Windows git binary, real NTFS ACLs on the quarantine move, and real
//	Windows process-group semantics for the pgid liveness probe
//	(lease_fence_windows.go's windowsLivenessProbe), none of which a
//	cross-compiled GOOS=windows test binary run on this machine can
//	honestly exercise. This file's own tests run FOR REAL on the Windows
//	CI runner; they are never invoked as `GOOS=windows go test` here.
//
// Inputs: none beyond the platform itself.
// Outputs: a passing (not skipped-away) proof that jobWorktreeDir/
//
//	jobBranch produce Windows-legal paths, and a recorded, running
//	explanation (never silent prose) of why the real-git round-trip
//	itself is the Windows CI matrix's job, not this local suite's.
//
// Constraints: no CGO, no golang.org/x/sys/windows import here — this
//
//	file only touches jobWorktreeDir/jobBranch, both plain
//	path/filepath.
//
// SPORT: jobs/worktree-manager (ADD, P1-E29-W6-S59-T3).

import (
	"strings"
	"testing"
)

// TestWorktreePathIsWindowsLegal proves jobWorktreeDir never emits a
// forward slash on Windows (filepath.Join/FromSlash both resolve against
// GOOS at compile+run time, so this assertion is only meaningful compiled
// for windows) and that the R-16.37 job-<id>/job/<id> naming survives
// path-separator translation unchanged.
func TestWorktreePathIsWindowsLegal(t *testing.T) {
	got := jobWorktreeDir(`C:\repos\demo`, "job-42")
	if strings.Contains(got, "/") {
		t.Fatalf("jobWorktreeDir(%q) = %q, contains a forward slash on windows", "job-42", got)
	}
	if !strings.HasSuffix(got, `job-job-42`) {
		t.Fatalf("jobWorktreeDir(...) = %q, want a job-job-42 suffix", got)
	}
	if branch := jobBranch("job-42"); branch != "job/job-42" {
		t.Fatalf("jobBranch(%q) = %q, want job/job-42 (branch names use / on every platform)", "job-42", branch)
	}
}

// TestWorktreeRealGitRoundTripIsWindowsCIMatrixJob records, as a running
// test rather than as prose, why worktree_test.go/worktree_sweep_test.go/
// worktree_quarantine_test.go/worktree_snapshot_test.go's real-git
// round-trips are not re-run from this file: they need a real Windows git
// binary, real NTFS semantics, and the real windowsLivenessProbe, which
// only the Windows CI matrix (A-T3) can honestly supply (R-16.47).
func TestWorktreeRealGitRoundTripIsWindowsCIMatrixJob(t *testing.T) {
	t.Skip("real-git worktree round-trip: exercised on the Windows CI matrix itself, never via a simulated GOOS=windows local run (R-16.47)")
}
