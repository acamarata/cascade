//go:build windows

// Package nodes (records_file_windows.go): Purpose: the windows half of
//
//	the record file backend. readRecordFile opens devices.json with
//	FILE_SHARE_DELETE added (fleet/tailer's openTranscript precedent), the
//	least restrictive handle a reader can hold. isTransientShareError
//	classifies the conflicts that can still occur: ERROR_SHARING_VIOLATION from
//	an open racing MoveFileEx, and ERROR_ACCESS_DENIED only from the
//	rename itself (another process's plain os.Open holds the target). An
//	ERROR_ACCESS_DENIED from an open or a mkdir is a real ACL denial and
//	returns at once. A rename denied by an ACL on the target itself looks
//	identical to a conflict, so it is retried for the bounded budget and
//	then returned unchanged (a directory ACL denial already fails earlier,
//	creating the temp file, and is not retried).
//
// SPORT: internal/nodes records file backend (CHG): windows sharing retry.
package nodes

import (
	"errors"
	"io"
	"os"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

// isTransientShareError reports whether err is one of the conflicts a
// concurrent open or rename of the record file produces.
func isTransientShareError(err error) bool {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return false
	}
	if errno == windows.ERROR_SHARING_VIOLATION {
		return true
	}
	if errno != windows.ERROR_ACCESS_DENIED {
		return false
	}
	var linkErr *os.LinkError
	return errors.As(err, &linkErr) && linkErr.Op == "rename"
}

// foldLockKey upper-cases the lock key: C:\X and c:\x name one file on
// Windows, so they must share one lock. NTFS compares names by
// upper-casing them, so ToUpper is the closer match.
func foldLockKey(key string) string { return strings.ToUpper(key) }

// readRecordFile reads path with READ|WRITE|DELETE sharing, falling back
// to os.ReadFile when the raw-path open reports not-exist (long paths; see
// readWithFallback).
func readRecordFile(path string) ([]byte, error) {
	return readWithFallback(readShareDelete, path)
}

// readShareDelete opens path with READ|WRITE|DELETE sharing. Errors come
// back as *os.PathError, so errors.Is(err, fs.ErrNotExist) still holds.
func readShareDelete(path string) ([]byte, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	share := uint32(windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE | windows.FILE_SHARE_DELETE)
	h, err := windows.CreateFile(p, windows.GENERIC_READ, share, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	f := os.NewFile(uintptr(h), path)
	defer func() { _ = f.Close() }()
	return io.ReadAll(f)
}
