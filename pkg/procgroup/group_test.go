//go:build !windows

// Purpose: prove Lookup returns the real group (grandchild included) and a
//
//	typed no-such-process error, and that Exists and Empty split a live
//	group, an unreaped zombie and a reaped group the way kill(2) does.
//
// Inputs: /bin/sh started through syscall.ForkExec (no os/exec here).
// Outputs: test verdicts only.
// Constraints: probes a reaped leader's pgid only with signal 0, never a
//
//	real signal (R134).
//
// SPORT: pkg/procgroup (TEST) — P1-AGT-01.

package procgroup

import (
	"errors"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestProcgroupLookup(t *testing.T) {
	leader, grandchild := startGroupLeader(t)
	defer func() {
		_ = Signal(leader, SigKill)
		var ws syscall.WaitStatus
		_, _ = syscall.Wait4(leader, &ws, 0, nil)
	}()
	for _, pid := range []int{leader, grandchild} {
		if pgid, err := Lookup(pid); err != nil || pgid != leader {
			t.Fatalf("Lookup(%d) = %d, %v; want the leader's group %d", pid, pgid, err, leader)
		}
	}
	_, err := Lookup(1 << 30)
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindNotFound {
		t.Fatalf("Lookup(no such pid) = %v, want KindNotFound", err)
	}
	var nsp *NoSuchProcessError
	if !IsNoSuchProcess(err) || !errors.As(err, &nsp) || nsp.Pid != 1<<30 || !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("Lookup(no such pid) = %v, want a NoSuchProcessError for the pid and ESRCH in the chain", err)
	}
	if IsNoSuchProcess(cascade.Wrapf(cascade.KindNotFound, syscall.ESRCH, "x")) || IsNoSuchProcess(nil) {
		t.Fatal("IsNoSuchProcess matched an error that holds no NoSuchProcessError")
	}
	for _, pid := range []int{-1, 0, 1 << 40} {
		_, err := Lookup(pid)
		if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
			t.Errorf("Lookup(%d) = %v, want KindInvalidInput", pid, err)
		}
		if IsNoSuchProcess(err) {
			t.Errorf("Lookup(%d) reported no such process for a refused pid", pid)
		}
	}
}

// startLoneLeader starts /bin/sh as its own group leader, blocked in the
// read builtin on a pipe this test holds, so it has no children: once it
// is reaped the group is empty without relying on PID 1 to reap an orphan
// (a container without an init never does). The cleanup kills and reaps
// it only if the test has not reaped it yet (R134).
func startLoneLeader(t *testing.T) (pid int, reaped *bool) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	pid, err = syscall.ForkExec("/bin/sh", []string{"sh", "-c", "read -r line"}, &syscall.ProcAttr{
		Env: []string{}, Files: []uintptr{r.Fd(), 1, 2}, Sys: Attr(),
	})
	_ = r.Close()
	if err != nil {
		_ = w.Close()
		t.Fatalf("fork /bin/sh: %v", err)
	}
	reaped = new(bool)
	t.Cleanup(func() {
		_ = w.Close()
		if !*reaped {
			_ = Signal(pid, SigKill)
			var ws syscall.WaitStatus
			_, _ = syscall.Wait4(pid, &ws, 0, nil)
		}
	})
	return pid, reaped
}

func TestProcgroupExistsEmpty(t *testing.T) {
	leader, reaped := startLoneLeader(t)
	if !Exists(leader) || Empty(leader) {
		t.Fatalf("live group %d: Exists %v Empty %v, want true false", leader, Exists(leader), Empty(leader))
	}
	if err := Signal(leader, SigKill); err != nil {
		t.Fatalf("Signal(SigKill) = %v", err)
	}
	// The killed leader stays a zombie until reaped: never empty yet.
	time.Sleep(50 * time.Millisecond)
	if !Exists(leader) || Empty(leader) {
		t.Fatalf("unreaped group %d: Exists %v Empty %v, want true false", leader, Exists(leader), Empty(leader))
	}
	var ws syscall.WaitStatus
	if _, err := syscall.Wait4(leader, &ws, 0, nil); err != nil {
		t.Fatalf("Wait4(leader): %v", err)
	}
	*reaped = true
	if !Empty(leader) || Exists(leader) {
		t.Fatalf("reaped lone group %d: Exists %v Empty %v, want false true", leader, Exists(leader), Empty(leader))
	}
}

func TestProcgroupExistsEmpty_RefusesMisreadPgids(t *testing.T) {
	for _, pgid := range []int{-5, 0, 1, 1 << 40} {
		if Exists(pgid) || Empty(pgid) {
			t.Errorf("pgid %d: Exists %v Empty %v, want false false", pgid, Exists(pgid), Empty(pgid))
		}
	}
	if SigTerm != syscall.SIGTERM || SigKill != syscall.SIGKILL {
		t.Fatal("SigTerm/SigKill do not match the platform signals")
	}
}
