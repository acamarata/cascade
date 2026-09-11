//go:build windows

// Package tailer (tailer_windows.go): Purpose: the Windows half of Tailer's file open. Go's os.Open on
//
//	Windows calls CreateFile with dwShareMode =
//	FILE_SHARE_READ|FILE_SHARE_WRITE — it deliberately omits
//	FILE_SHARE_DELETE, so while this tailer holds the handle open, the
//	harness process that owns the transcript cannot rename it (log
//	rotation) or remove it, failing with ERROR_SHARING_VIOLATION. That
//	is a real production defect on Windows, not a test artifact:
//	rotation is exactly the scenario this package exists to survive
//	(see tailer.go's package doc). This file opens the handle directly
//	via windows.CreateFile with FILE_SHARE_DELETE added, then wraps it
//	as a normal *os.File (os.NewFile) so every other method on Tailer
//	(Read, Seek, Stat, Close) is unchanged Go stdlib code.
//
// Constraints: golang.org/x/sys is already a DIRECT go.mod requirement
//
//	(providers/sqlite/flock_windows.go); x/sys/windows is the same
//	module, so this file adds no new dependency and no new license
//	entry.
//
// SPORT: fleet/tailer (CHANGED, windows-parity-pass-4).
package tailer

import (
	"os"

	"golang.org/x/sys/windows"
)

// openTranscript opens path for reading with FILE_SHARE_DELETE included,
// so a concurrent rename or remove by the writing harness succeeds while
// this handle stays open. See this file's doc comment and tailer_unix.go
// for the POSIX side, where no such share flag exists to set.
func openTranscript(path string) (*os.File, error) {
	namePtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	shareMode := uint32(windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE | windows.FILE_SHARE_DELETE)
	handle, err := windows.CreateFile(
		namePtr,
		windows.GENERIC_READ,
		shareMode,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(handle), path), nil
}
