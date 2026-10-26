//go:build linux

package agents

// Purpose: HideDaemonEnviron marks the daemon process non-dumpable so that
// /proc/<daemon>/environ, which holds every provider key under AUTHORIZATIONS
// E.1, is unreadable to same-uid peers (the file becomes root-owned and mode
// 0400; the kernel then denies a same-uid reader).
//
// Constraints: the daemon keeps reading its own /proc entries (a process may
// always inspect its own thread group), but it can no longer produce a core
// dump and a same-uid debugger cannot attach. The flag resets to dumpable on
// execve of a readable binary, so the agent drivers the daemon spawns keep
// their normal /proc entries and the lease sweep still reads them. It does not
// help on darwin (no equivalent) and does not stop a process that can read the
// daemon's memory by other means (root, ptrace by a capability holder).

import (
	"golang.org/x/sys/unix"

	"github.com/acamarata/cascade/pkg/cascade"
)

// HideDaemonEnviron calls prctl(PR_SET_DUMPABLE, 0) and reads the flag back;
// a failure of either is KindUnavailable and the caller decides whether to
// start the daemon anyway.
func HideDaemonEnviron() error {
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "agents: the daemon environment could not be hidden")
	}
	got, err := unix.PrctlRetInt(unix.PR_GET_DUMPABLE, 0, 0, 0, 0)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "agents: the dumpable flag could not be read back")
	}
	if got != 0 {
		return cascade.New(cascade.KindUnavailable, "agents: the daemon is still dumpable after PR_SET_DUMPABLE 0")
	}
	return nil
}
