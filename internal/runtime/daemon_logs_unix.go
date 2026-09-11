//go:build !windows

// Package runtime (daemon_logs_unix.go): Purpose: the POSIX half of DaemonLogsHandler's file open. Rename and
//
//	unlink of an open file are always permitted on POSIX, so a plain
//	os.Open already gives daemon_logs.go the share semantics rotation.go
//	and the tests both rely on. See daemon_logs.go's Constraints comment
//	and daemon_logs_windows.go for why Windows differs.
//
// SPORT: runtime/logger (CHANGED, windows-parity-pass-4).
package runtime

import "os"

// openLogFile opens path for reading.
func openLogFile(path string) (*os.File, error) {
	return os.Open(path)
}
