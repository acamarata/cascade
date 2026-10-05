//go:build !windows

// Package runtime (atomic_write_unix.go): Purpose: the POSIX half of the
//
//	atomic file write. A rename is durable only once the directory entry
//	that names it is on disk, so the directory is opened and fsynced after
//	every publish. A failure is returned, never skipped: a filesystem that
//	refuses a directory fsync cannot promise the rename survives a crash.
//
// SPORT: runtime/atomic-file-write (ADD, P1-CORE-08).
package runtime

import "os"

// syncDirectory fsyncs dir so a rename or link inside it is durable.
func syncDirectory(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	syncErr := d.Sync()
	closeErr := d.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

// restoreLinkedMode is a no-op on POSIX: permission bits live on the inode
// both links share, and removing the temp link does not touch them.
func restoreLinkedMode(string, os.FileMode) error { return nil }
