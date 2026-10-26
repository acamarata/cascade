//go:build darwin

package agents

// Purpose: the darwin peer inspector against this process and against a
// process that no longer exists.
// Constraints: no socket, no spawned process; the entry checked is one this
// process was started with, which kern.procargs2 returns for its own pid.

import (
	"os"
	"syscall"
	"testing"
)

func TestSysctlInspectorOnThisProcess(t *testing.T) {
	insp, err := platformPeerInspector()
	if err != nil {
		t.Fatal(err)
	}
	if ppid, err := insp.parent(os.Getpid()); err != nil || ppid != os.Getppid() {
		t.Errorf("parent(self) = %d, %v, want %d", ppid, err, os.Getppid())
	}
	if pgid, sid, err := insp.groups(os.Getpid()); err != nil || pgid != syscall.Getpgrp() || sid <= 0 {
		t.Errorf("groups(self) = %d %d, %v, want pgid %d and a session", pgid, sid, err, syscall.Getpgrp())
	}
	if held, err := insp.holds(os.Getpid(), []string{"CASCADE_NOT_SET_ANYWHERE=1", os.Environ()[0]}); err != nil || !held {
		t.Errorf("holds(self, first environ entry) = %v, %v, want true", held, err)
	}
	if held, err := insp.holds(os.Getpid(), []string{"CASCADE_NOT_SET_ANYWHERE=1"}); err != nil || held {
		t.Errorf("holds(self, absent entry) = %v, %v, want false", held, err)
	}
}

func TestSysctlInspectorFailsOnAMissingProcess(t *testing.T) {
	insp, _ := platformPeerInspector()
	const none = 99999999
	if _, err := insp.holds(none, []string{"A=b"}); err == nil {
		t.Error("holds of a missing pid returned no error, want a refusal-worthy failure")
	}
	if _, err := insp.parent(none); err == nil {
		t.Error("parent of a missing pid returned no error")
	}
	if _, _, err := insp.groups(none); err == nil {
		t.Error("groups of a missing pid returned no error")
	}
}
