package agents

// Purpose: the linux source's owner re-check at the signal boundary.
//
// Constraints: reads /proc only; nothing is spawned or signalled.

import (
	"os"
	"strconv"
	"syscall"
	"testing"
)

func TestOwnedByUsFollowsTheProcEntryOwner(t *testing.T) {
	p := procfsSource{root: procRoot}
	if !p.ownedByUs(os.Getpid()) {
		t.Fatal("ownedByUs(self) = false, want true")
	}
	if p.ownedByUs(1 << 30) {
		t.Fatal("ownedByUs(no such pid) = true, want false")
	}
	// pid 1 is normally root's; where it is not ours the check must say so.
	info, err := os.Stat(procRoot + "/" + strconv.Itoa(1))
	if err != nil {
		t.Skip("no /proc/1")
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Skip("no owner on /proc/1")
	}
	if want := st.Uid == uint32(os.Geteuid()); p.ownedByUs(1) != want {
		t.Fatalf("ownedByUs(1) = %v, want %v", p.ownedByUs(1), want)
	}
}
