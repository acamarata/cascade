package agents

// Purpose: darwin-only sweep facts that need the real kernel: a process that
// was already stopped is left stopped by a kill that does not match, and the
// environment of an Apple-signed system binary is invisible to the sweep (a
// documented residual, pinned so a change to it is deliberate).
//
// Constraints: only testdata/reapchild and /bin/sleep are spawned; HOME is a
// fresh temp dir; every child is killed at cleanup.

import (
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// pStat reads the BSD process state of pid.
func pStat(t *testing.T, pid int) int8 {
	t.Helper()
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		t.Fatalf("kinfo_proc for %d: %v", pid, err)
	}
	return kp.Proc.P_stat
}

func TestDarwinKillLeavesAnAlreadyStoppedProcessStopped(t *testing.T) {
	bin := reapChildBin(t)
	mine, _ := NewLeaseMarker()
	other, _ := NewLeaseMarker()
	pidfile := filepath.Join(t.TempDir(), "p.pid")
	target := startChild(t, bin, mine, "sleep", pidfile)
	pid := readPid(t, pidfile)
	if err := syscall.Kill(pid, syscall.SIGSTOP); err != nil {
		t.Fatalf("SIGSTOP: %v", err)
	}
	for deadline := time.Now().Add(2 * time.Second); pStat(t, pid) != statStopped && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
	}
	if err := (sysctlSource{}).kill(pid, leaseEntry(other)); err != nil {
		t.Fatalf("kill with another marker's entry = %v, want nil", err)
	}
	if got := pStat(t, pid); got != statStopped {
		t.Fatalf("p_stat after a non-matching kill = %d, want %d (stopped): the sweep resumed a process it did not own", got, statStopped)
	}
	if err := (sysctlSource{}).kill(pid, leaseEntry(mine)); err != nil {
		t.Fatalf("kill with the process's own entry = %v, want nil", err)
	}
	select {
	case <-target.done:
	case <-time.After(10 * time.Second):
		t.Fatal("a stopped process that carries the entry survived the kill")
	}
}

func TestDarwinAppleSignedBinaryEnvironmentIsAKnownResidual(t *testing.T) {
	marker, _ := NewLeaseMarker()
	cmd := exec.Command("/bin/sleep", "30")
	cmd.Env = childEnv(t, marker)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start /bin/sleep: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	has, err := (sysctlSource{}).hasEntry(cmd.Process.Pid, leaseEntry(marker))
	if err != nil {
		t.Fatalf("hasEntry: %v", err)
	}
	if has {
		t.Skip("this kernel returns the environment of Apple-signed binaries: the residual in " +
			"docs/security-posture/driver-containment.md is smaller here; update it")
	}
}
