//go:build !windows

package jobs

// Purpose: closes the coverage gap on lease_fence_unix.go's
//   NewProcessLivenessProbe/IsAlive, which had no test at all. Follows
//   internal/runtime/lockfile_processalive_unix_test.go's precedent: a
//   REAL process, never a mocked syscall -- a probe test that mocks the
//   syscall it exists to call would only prove the mock works.
// Inputs: the current process's own process group (alive case) and a
//   freshly-spawned, already-reaped child's process group (dead case).
// Outputs: n/a (test file).
// Constraints: must carry the SAME build tag as lease_fence_unix.go
//   (!windows) -- an untagged test over a tagged implementation
//   compiles on windows, where NewProcessLivenessProbe/unixLivenessProbe
//   do not exist, and fails there with undefined symbols.
// SPORT: jobs/lease-model (FIX, coverage floor restoration).

import (
	"os/exec"
	"syscall"
	"testing"
)

// TestProcessLivenessProbe_SelfIsAlive proves IsAlive reports true for
// this test process's own process group -- the positive case a
// mocked-syscall test could not distinguish from a bug that always
// returns true.
func TestProcessLivenessProbe_SelfIsAlive(t *testing.T) {
	pgid, err := syscall.Getpgid(0)
	if err != nil {
		t.Fatalf("Getpgid(self): %v", err)
	}
	probe := NewProcessLivenessProbe()
	if !probe.IsAlive(int64(pgid)) {
		t.Fatalf("IsAlive(self pgid %d) = false, want true", pgid)
	}
}

// TestProcessLivenessProbe_ReapedChildIsDead spawns a child in its OWN
// process group (Setpgid), waits for it to exit and be reaped, then
// asserts IsAlive reports false for that now-gone group -- the actual
// negative constructed against a real process, not inferred from the
// positive case.
func TestProcessLivenessProbe_ReapedChildIsDead(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Run(); err != nil {
		t.Fatalf("spawn+run short-lived child: %v", err)
	}
	deadPgid := cmd.Process.Pid // Setpgid: true makes the child its own group leader, pgid == pid

	probe := NewProcessLivenessProbe()
	if probe.IsAlive(int64(deadPgid)) {
		t.Fatalf("IsAlive(reaped child's pgid %d) = true, want false", deadPgid)
	}
}

// TestProcessLivenessProbe_NonPositivePgidNeverAlive asserts the
// defensive pgid<=0 guard (documented on unixLivenessProbe.IsAlive):
// never alive by construction, no syscall involved.
func TestProcessLivenessProbe_NonPositivePgidNeverAlive(t *testing.T) {
	probe := NewProcessLivenessProbe()
	for _, pgid := range []int64{0, -1, -4242} {
		if probe.IsAlive(pgid) {
			t.Errorf("IsAlive(%d) = true, want false (non-positive pgid)", pgid)
		}
	}
}
