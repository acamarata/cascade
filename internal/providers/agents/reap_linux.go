package agents

// Purpose: the linux process source: pids from /proc, environments from
// /proc/<pid>/environ.
//
// Constraints: only processes whose /proc entry is owned by this uid are
// listed. A zombie's environ reads empty, so it never matches.

import (
	"os"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"
)

// procRoot is the procfs mount.
const procRoot = "/proc"

// procfsSource reads a procfs tree.
type procfsSource struct{ root string }

// platformSource is the /proc source.
func platformSource() (procSource, error) { return procfsSource{root: procRoot}, nil }

// list returns the numeric /proc entries owned by this uid.
func (p procfsSource) list() ([]int, error) {
	entries, err := os.ReadDir(p.root)
	if err != nil {
		return nil, err
	}
	uid := uint32(os.Geteuid())
	var out []int
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if st, ok := info.Sys().(*syscall.Stat_t); ok && st.Uid == uid {
			out = append(out, pid)
		}
	}
	return out, nil
}

// hasEntry reads /proc/<pid>/environ.
func (p procfsSource) hasEntry(pid int, entry string) (bool, error) {
	blob, err := os.ReadFile(p.root + "/" + strconv.Itoa(pid) + "/environ")
	if err != nil {
		if noEntries(err) {
			return false, nil
		}
		return false, err
	}
	return environHas(blob, entry), nil
}

// ownedByUs reports whether /proc/<pid> is currently owned by this uid, so a
// process that changed credentials since the listing is not signalled.
func (p procfsSource) ownedByUs(pid int) bool {
	info, err := os.Stat(p.root + "/" + strconv.Itoa(pid))
	if err != nil {
		return false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && st.Uid == uint32(os.Geteuid())
}

// kill signals through a pidfd opened before the entry is re-checked: the
// descriptor names the process that owned the pid when it was opened, so a pid
// recycled after the check can never receive the signal. Only a kernel (or
// sandbox) that has no pidfd at all (ENOSYS, EPERM) falls back to a check then
// a signal by pid, which narrows the window without closing it; any other
// failure to open the descriptor (EMFILE, ENOMEM) fails closed and the re-list
// reports the survivor.
func (p procfsSource) kill(pid int, entry string) error {
	fd, err := unix.PidfdOpen(pid, 0)
	switch err {
	case nil:
		defer func() { _ = unix.Close(fd) }()
	case syscall.ESRCH:
		return nil
	case syscall.ENOSYS, syscall.EPERM:
		fd = -1
	default:
		return err
	}
	has, herr := p.hasEntry(pid, entry)
	if herr != nil || !has || !p.ownedByUs(pid) {
		return herr
	}
	if fd < 0 {
		err = syscall.Kill(pid, syscall.SIGKILL)
	} else {
		err = unix.PidfdSendSignal(fd, unix.SIGKILL, nil, 0)
	}
	if err != nil && err != syscall.ESRCH {
		return err
	}
	return nil
}
