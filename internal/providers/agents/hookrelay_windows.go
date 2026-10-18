//go:build windows

// Purpose: the Windows half of the hook relay: LockFileEx for the Seq
//
//	counter and the (weaker) trust checks.
//
// Inputs: an open counter file; file info and the caller's uid (-1 on
//
//	Windows); a directory and a name inside it.
//
// Outputs: lock state; trust verdicts; an open file.
// Constraints: Windows has no uid, mode bits or link count to check, so
//
//	trust reduces to "a regular file", "a socket-like entry" and "a directory
//	and file that are not symlinks when examined"; the user profile ACLs are
//	the boundary there. The lock is a one-byte LockFileEx range, released
//	with the identical range before the handle closes.
//
// SPORT: internal.providers.agents/ADD (P1-AGT-14, contract:agent-hook-relay).

package agents

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// lockRangeBytes is the locked byte range; a zero length would lock nothing.
const lockRangeBytes = 1

// tryLockFile takes the exclusive lock without blocking; false means another
// holder has it.
func tryLockFile(f *os.File) (bool, error) {
	ol := new(windows.Overlapped)
	err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, lockRangeBytes, 0, ol)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, windows.ERROR_LOCK_VIOLATION):
		return false, nil
	}
	return false, err
}

// unlockFile releases the lock taken by tryLockFile.
func unlockFile(f *os.File) {
	_ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, lockRangeBytes, 0, new(windows.Overlapped))
}

// tokenFileTrusted reports whether fi is a regular file.
func tokenFileTrusted(fi os.FileInfo, _ int) bool {
	return fi.Mode().IsRegular()
}

// counterTrusted (the callback file and counter check) reports whether fi is
// a regular file; Windows reports no link count here.
func counterTrusted(fi os.FileInfo, uid int) bool {
	return tokenFileTrusted(fi, uid)
}

// socketTrusted accepts any entry that exists: the platform reports unix
// sockets inconsistently and the dial itself is the check.
func socketTrusted(os.FileInfo, int) bool {
	return true
}

// openUnderDir opens name inside dir after checking that dir is a real
// directory and name is not a symlink; see the file comment for the limits.
func openUnderDir(dir, name string, flag int, perm os.FileMode, _ int, _ bool) (*os.File, error) {
	if fi, err := os.Lstat(dir); err != nil || !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
		return nil, errDirUntrusted
	}
	path := filepath.Join(dir, name)
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return nil, errFileUnopenable
	}
	f, err := os.OpenFile(path, flag, perm)
	if err != nil {
		return nil, errFileUnopenable
	}
	return f, nil
}
