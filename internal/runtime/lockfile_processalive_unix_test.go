//go:build !windows

// Purpose: TestProcessAlive_SelfIsAlive/_ExitedProcessIsDead, split out
//   of lockfile_test.go because they spawn a real POSIX child ("/bin/sh")
//   and assert lockfile_unix.go's real kill(pid,0) probe - neither of
//   which exists or applies on Windows, where ProcessAlive is a
//   deliberate tier-2 stub (lockfile_windows.go's
//   errProcessAliveUnsupportedWindows; recovery.go never calls it there).
//   lockfile_processalive_windows_test.go carries the real Windows-side
//   assertion.

package runtime

import (
	"os"
	"os/exec"
	"testing"
)

func TestProcessAlive_SelfIsAlive(t *testing.T) {
	liveness, err := ProcessAlive(os.Getpid())
	if err != nil {
		t.Fatalf("ProcessAlive(self): unexpected error: %v", err)
	}
	if liveness != ProcessLivenessAlive {
		t.Fatalf("ProcessAlive(self) = %v, want ProcessLivenessAlive", liveness)
	}
}

func TestProcessAlive_ExitedProcessIsDead(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	if err := cmd.Run(); err != nil {
		t.Fatalf("spawn+run short-lived child: %v", err)
	}
	pid := cmd.Process.Pid

	liveness, err := ProcessAlive(pid)
	if err != nil {
		t.Fatalf("ProcessAlive(exited pid %d): unexpected error: %v", pid, err)
	}
	if liveness != ProcessLivenessDead {
		t.Fatalf("ProcessAlive(exited pid %d) = %v, want ProcessLivenessDead", pid, liveness)
	}
}
