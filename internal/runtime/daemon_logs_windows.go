//go:build windows

// Package runtime (daemon_logs_windows.go): Purpose: the Windows half of DaemonLogsHandler's file open. Go's
//
//	os.Open on Windows calls CreateFile with dwShareMode =
//	FILE_SHARE_READ|FILE_SHARE_WRITE — it omits FILE_SHARE_DELETE, so
//	while this handler holds the log file open (including across
//	followLoop's poll loop), rotation.go's rename-away-and-reopen fails
//	with ERROR_SHARING_VIOLATION. That is a real production defect on
//	Windows, not a test artifact: surviving exactly that rotation is
//	followLoop's job (daemon_logs.go's BLOCKING FIX 1 comment). This
//	file opens the handle directly via windows.CreateFile with
//	FILE_SHARE_DELETE added, then wraps it as a normal *os.File
//	(os.NewFile) so every other line of daemon_logs.go (io.Copy,
//	os.Stat, os.SameFile) is unchanged Go stdlib code. windows.Errno is
//	an alias for syscall.Errno (x/sys/windows/aliases.go), whose Is
//	method already maps ERROR_FILE_NOT_FOUND/ERROR_PATH_NOT_FOUND to
//	fs.ErrNotExist, so DaemonLogsHandler's existing os.IsNotExist(err)
//	check keeps working unchanged against the *os.PathError this
//	returns.
//
// Constraints: golang.org/x/sys is already a DIRECT go.mod requirement
//
//	(providers/sqlite/flock_windows.go); x/sys/windows is the same
//	module, so this file adds no new dependency and no new license
//	entry.
//
// SPORT: runtime/logger (CHANGED, windows-parity-pass-4).
package runtime

import (
	"os"

	"golang.org/x/sys/windows"
)

// openLogFile opens path for reading with FILE_SHARE_DELETE included, so
// a concurrent rotation rename or remove succeeds while this handle
// stays open. See this file's doc comment and daemon_logs_unix.go for
// the POSIX side, where no such share flag exists to set.
func openLogFile(path string) (*os.File, error) {
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
