//go:build windows

// Package runtime (atomic_write_windows.go): Purpose: the windows half of
//
//	the atomic file write. Windows has no portable way to open a directory
//	for FlushFileBuffers, and NTFS journals the rename's metadata itself,
//	so syncDirectory is a no-op. File modes map only to the read-only
//	attribute; os.Remove of the temp link clears that attribute on the file
//	both links share, so restoreLinkedMode puts it back on the published
//	file when perm grants no owner write.
//
// SPORT: runtime/atomic-file-write (ADD, P1-CORE-08).
package runtime

import "os"

// syncDirectory is a no-op on windows (see the file comment).
func syncDirectory(string) error { return nil }

// restoreLinkedMode re-applies a read-only perm after the temp link is
// removed, because that removal clears the shared read-only attribute.
func restoreLinkedMode(path string, perm os.FileMode) error {
	if perm&0o200 != 0 {
		return nil
	}
	return os.Chmod(path, perm)
}
