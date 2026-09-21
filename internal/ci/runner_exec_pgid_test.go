//go:build !windows

// Purpose: the process-TREE timeout proof. A step that backgrounds a
// grandchild outliving its own shell must still hit its deadline, and the
// grandchild must be dead afterwards -- the two properties a plain
// exec.CommandContext cancellation does NOT give (it signals the shell
// only, and then Wait blocks on the pipes the survivor still holds).
//
// This is a REAL sub-process test with a REAL orphan probe: the grandchild
// writes its own pid, and after the step returns the test asks the kernel
// whether that pid still exists (signal 0). Nothing here is faked --
// asserting on a recorded call could not distinguish a killed tree from a
// surviving one.
// SPORT: internal.ci.killProcessGroup/TESTED (P1-E25-W5-S51-T5).
package ci

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestShellExecutor_TimeoutKillsTheWholeTree runs a step whose shell
// backgrounds a long sleep and then sleeps itself -- the `sleep 30 & sleep
// 30` shape -- with a one-second timeout.
func TestShellExecutor_TimeoutKillsTheWholeTree(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "grandchild.pid")
	// exec replaces the inner shell with sleep itself, so the pid written
	// is the pid of the process that is actually still running -- not a
	// shell that has already exited and left an unrelated child behind.
	command := "sh -c 'echo $$ > " + pidFile + "; exec sleep 30' & sleep 30"

	start := time.Now()
	res := ShellExecutor{}.Run(context.Background(), ExecRequest{
		WorkDir: dir, Command: command, Env: AllowedEnv(testEnviron(), nil), Timeout: time.Second,
	})
	elapsed := time.Since(start)

	if !res.TimedOut {
		t.Fatalf("Run = %+v, want TimedOut", res)
	}
	if elapsed > 4*time.Second {
		t.Fatalf("Run took %s for a 1s timeout; a surviving grandchild holding the output pipes blocked Wait", elapsed)
	}
	if res.GroupKillErr != nil {
		t.Errorf("group kill reported %v; a unix process-group kill must succeed", res.GroupKillErr)
	}

	pid := readGrandchildPID(t, pidFile)
	if processAlive(pid) {
		// Do not leave the machine with an orphan even when the assertion
		// fails: the whole point of the test is that this pid should be
		// gone, so reap it before reporting.
		_ = syscall.Kill(pid, syscall.SIGKILL)
		t.Fatalf("grandchild pid %d survived the step's timeout; the deadline must kill the process GROUP, not just the shell", pid)
	}
}

// readGrandchildPID waits briefly for the pid file the backgrounded
// grandchild writes, then reads it. A short poll rather than a fixed sleep:
// the file appears as soon as the inner shell runs, which is immediately,
// and polling keeps the test from being slower than it has to be.
func readGrandchildPID(t *testing.T, pidFile string) int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		raw, err := os.ReadFile(pidFile)
		if err == nil {
			pid, convErr := strconv.Atoi(strings.TrimSpace(string(raw)))
			if convErr == nil && pid > 0 {
				return pid
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the backgrounded grandchild never wrote %s; the test cannot prove anything about its fate", pidFile)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// processAlive reports whether pid still exists, via signal 0 -- the
// standard existence probe, which delivers nothing and only reports
// whether the target could be signalled. ESRCH means gone.
func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return !errors.Is(err, syscall.ESRCH)
}
