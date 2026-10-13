//go:build !windows

package claude

// Purpose: the process-group helpers in driver_unix.go: the strict group
//   lookup (a vanished leader whose group is still live), the pgid-1
//   refusal, the zombie-aware empty test and the straggler audit.
// Constraints: SCOPE DEVIATION (declared): not in files_scope.add; tests
//   driver_unix.go. Moved out of guard_test.go to keep it under 300 lines.
// SPORT: providers.agents.claude tests (P1-AGT-01).

import (
	"os"
	"os/exec"
	"runtime"
	"testing"

	"github.com/acamarata/cascade/pkg/procgroup"
)

func TestProcessGroupHelpers(t *testing.T) {
	c := exec.Command("sleep", "30")
	setProcessGroup(c)
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Process.Kill(); _, _ = c.Process.Wait() }()
	orig := getpgid
	defer func() { getpgid = orig }()
	getpgid = func(int) (int, error) { return procgroup.Lookup(1 << 30) }
	if pg, err := resolvePgid(c.Process.Pid); err != nil || pg != c.Process.Pid {
		t.Fatalf("a live group behind an ESRCH lookup: %d, %v", pg, err)
	}
	if _, err := resolvePgid(1 << 30); err == nil {
		t.Fatal("resolvePgid accepted a pid with no group")
	}
	if signalGroup(1, procgroup.SigTerm) == nil || groupIsEmpty(1) {
		t.Fatal("pgid 1 must never be signalled as a group")
	}
	if !hasEnvVar([]byte("A=1\x00"+VendorConfigDirVar+"=/x\x00"), VendorConfigDirVar) || hasEnvVar([]byte("A=1"), "A_") {
		t.Fatal("hasEnvVar")
	}
	z := exec.Command("true") // exits at once; unreaped it is a zombie
	setProcessGroup(z)
	if err := z.Start(); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the child to exit", func() bool { return !processRunnable(z.Process.Pid) })
	if groupIsEmpty(z.Process.Pid) {
		t.Fatal("a group still holding an unreaped zombie was reported empty")
	}
	// darwin cannot read a member's environment, so an unreaped member
	// (EPERM there) is unprovable; linux reads /proc and skips zombies.
	if got, want := auditStragglers(z.Process.Pid, []string{VendorConfigDirVar}), runtime.GOOS != "linux"; got != want {
		t.Fatalf("auditStragglers(zombie-only group) = %v, want %v on %s", got, want, runtime.GOOS)
	}
	_, _ = z.Process.Wait()
	eventually(t, "the reaped group to read empty", func() bool { return groupIsEmpty(z.Process.Pid) })
	if auditStragglers(z.Process.Pid, []string{VendorConfigDirVar}) {
		t.Fatal("auditStragglers reported a straggler in an empty group")
	}
	if liveMember(os.Getpid(), 1<<30) {
		t.Fatal("liveMember matched a group this process is not in")
	}
}
