//go:build !windows

// Purpose: prove Attr makes a child its own group leader and Signal reaches
//
//	the whole group (grandchild included), maps ESRCH to nil, and refuses
//	the pgids kill(2) would misread.
//
// Inputs: /bin/sh started through syscall.ForkExec (no os/exec here).
// Outputs: test verdicts only.
// Constraints: never signals a group whose leader this test already
//
//	reaped (R134): the ESRCH case targets a pgid no system allocates.
//
// SPORT: pkg/procgroup (TEST) — P1-PLG-09.

package procgroup

import (
	"bufio"
	"errors"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// startGroupLeader forks `sh -c 'sleep 30 & echo $!; wait'` as a new group
// leader and returns the shell's pid and the backgrounded sleeper's pid.
func startGroupLeader(t *testing.T) (leader, grandchild int) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer func() { _ = r.Close() }()
	pid, err := syscall.ForkExec("/bin/sh", []string{"sh", "-c", "sleep 30 & echo $!; wait"}, &syscall.ProcAttr{
		Env: []string{}, Files: []uintptr{0, w.Fd(), 2}, Sys: Attr(),
	})
	_ = w.Close()
	if err != nil {
		t.Fatalf("fork /bin/sh: %v", err)
	}
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil {
		t.Fatalf("reading grandchild pid: %v", err)
	}
	gpid, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Fatalf("grandchild pid %q: %v", line, err)
	}
	return pid, gpid
}

// gone reports whether pid no longer runs: ESRCH, or (linux) a zombie that
// a non-reaping init (a container's PID 1) has not collected yet.
func gone(pid int) bool {
	if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
		return true
	}
	if runtime.GOOS != "linux" {
		return false
	}
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return true
	}
	fields := strings.Fields(string(raw[strings.LastIndexByte(string(raw), ')')+1:]))
	return len(fields) > 0 && fields[0] == "Z"
}

func TestProcgroupSignal(t *testing.T) {
	leader, grandchild := startGroupLeader(t)
	if pgid, err := syscall.Getpgid(leader); err != nil || pgid != leader {
		t.Fatalf("Getpgid(leader) = %d, %v; want %d (Attr must make the child its own group leader)", pgid, err, leader)
	}
	if pgid, err := syscall.Getpgid(grandchild); err != nil || pgid != leader {
		t.Fatalf("Getpgid(grandchild) = %d, %v; want the leader's group %d", pgid, err, leader)
	}
	if err := Signal(leader, syscall.SIGKILL); err != nil {
		t.Fatalf("Signal(live group) = %v, want nil", err)
	}
	var ws syscall.WaitStatus
	if _, err := syscall.Wait4(leader, &ws, 0, nil); err != nil {
		t.Fatalf("Wait4(leader): %v", err)
	}
	if !ws.Signaled() || ws.Signal() != syscall.SIGKILL {
		t.Fatalf("leader status = %v, want killed by SIGKILL", ws)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !gone(grandchild) {
		if time.Now().After(deadline) {
			_ = syscall.Kill(grandchild, syscall.SIGKILL)
			t.Fatalf("grandchild %d survived a SIGKILL to its group", grandchild)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestProcgroupSignal_NoSuchGroupIsNil(t *testing.T) {
	// 1<<30 is above every pid_max a kernel allocates (linux caps at
	// 4194304, darwin at 99998), so no live group can own it.
	if err := Signal(1<<30, syscall.Signal(0)); err != nil {
		t.Fatalf("Signal(no such group) = %v, want nil (ESRCH is success)", err)
	}
}

func TestProcgroupSignal_RefusesMisreadPgids(t *testing.T) {
	for _, pgid := range []int{-5, 0, 1, 1 << 40} {
		err := Signal(pgid, syscall.Signal(0))
		if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
			t.Errorf("Signal(%d) = %v, want KindInvalidInput", pgid, err)
		}
	}
}

func TestAttrSetsNewGroup(t *testing.T) {
	a := Attr()
	if a == nil || !a.Setpgid {
		t.Fatalf("Attr() = %+v, want Setpgid true", a)
	}
	if Attr() == a {
		t.Fatal("Attr() returned a shared value; a caller mutating it would leak into every other caller")
	}
}
