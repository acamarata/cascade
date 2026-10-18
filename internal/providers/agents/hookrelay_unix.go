//go:build !windows

// Purpose: the POSIX half of the hook relay: the non-blocking exclusive
//
//	flock the Seq counter needs, the owner and mode checks on the callback
//	file, the counter and the socket, and the directory-relative open that
//	keeps a symlinked directory from steering the relay to another tree.
//
// Inputs: an open counter file; file info and the caller's uid; a directory
//
//	and a name inside it.
//
// Outputs: lock state; trust verdicts; an open file.
// Constraints: flock is advisory and per open file description, so two relay
//
//	processes (or two goroutines) that each open the counter exclude each
//	other. A file with no owner information is not trusted. openUnderDir
//	opens the directory with O_NOFOLLOW|O_DIRECTORY, checks the opened
//	descriptor, then opens the name with openat and O_NOFOLLOW, so neither
//	the directory nor the file can be swapped for a symlink between check
//	and use; O_NONBLOCK keeps a FIFO from blocking the open and is cleared
//	before the file is handed back.
//
// SPORT: internal.providers.agents/ADD (P1-AGT-14, contract:agent-hook-relay).

package agents

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

// tryLockFile takes the exclusive lock without blocking; false means another
// holder has it.
func tryLockFile(f *os.File) (bool, error) {
	err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, unix.EWOULDBLOCK), errors.Is(err, unix.EINTR):
		return false, nil
	}
	return false, err
}

// unlockFile releases the lock; closing the file would too.
func unlockFile(f *os.File) {
	_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
}

// fileOwner returns the uid that owns fi. It is a variable so a test can
// make one named file look foreign while every other file keeps its owner.
var fileOwner = func(fi os.FileInfo) (int, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(st.Uid), true
}

// ownedBy reports whether fi is owned by uid.
func ownedBy(fi os.FileInfo, uid int) bool {
	owner, ok := fileOwner(fi)
	return ok && uid >= 0 && owner == uid
}

// tokenFileTrusted reports whether fi is a regular file with mode exactly
// 0600 owned by uid.
func tokenFileTrusted(fi os.FileInfo, uid int) bool {
	return fi.Mode().IsRegular() && fi.Mode().Perm() == 0o600 && ownedBy(fi, uid)
}

// counterTrusted is tokenFileTrusted plus exactly one link, the check for the
// callback file and the counter: a hard link to another tree's callback
// file is never read, and another file is never overwritten with a record.
func counterTrusted(fi os.FileInfo, uid int) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && st.Nlink == 1 && tokenFileTrusted(fi, uid)
}

// socketTrusted reports whether fi is a unix socket owned by uid.
func socketTrusted(fi os.FileInfo, uid int) bool {
	return fi.Mode()&os.ModeSocket != 0 && ownedBy(fi, uid)
}

// openUnderDir opens name inside dir. dir must be a real directory (not a
// symlink) owned by uid and, when private, with no group or other bits;
// otherwise errDirUntrusted. A failed open of name is errFileUnopenable.
func openUnderDir(dir, name string, flag int, perm os.FileMode, uid int, private bool) (*os.File, error) {
	dfd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, errDirUntrusted
	}
	d := os.NewFile(uintptr(dfd), dir)
	defer func() { _ = d.Close() }()
	fi, err := d.Stat()
	if err != nil || !fi.IsDir() || !ownedBy(fi, uid) || (private && fi.Mode().Perm()&0o077 != 0) {
		return nil, errDirUntrusted
	}
	fd, err := unix.Openat(int(d.Fd()), name, flag|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, uint32(perm.Perm()))
	if err != nil {
		return nil, errFileUnopenable
	}
	if err := unix.SetNonblock(fd, false); err != nil {
		_ = unix.Close(fd)
		return nil, errFileUnopenable
	}
	return os.NewFile(uintptr(fd), filepath.Join(dir, name)), nil
}
