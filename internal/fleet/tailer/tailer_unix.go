//go:build !windows

// Package tailer (tailer_unix.go): Purpose: the POSIX half of Tailer's file open. Rename and unlink of an
//
//	open file are always permitted on POSIX (the directory entry and the
//	open handle are independent; the inode survives until the last
//	handle closes), so a plain os.Open already gives tailer.go the
//	share semantics it needs — no platform call is needed here.
//
// SPORT: fleet/tailer (CHANGED, windows-parity-pass-4).
package tailer

import "os"

// openTranscript opens path for reading. See tailer.go's Constraints
// comment and tailer_windows.go for why this differs from Windows.
func openTranscript(path string) (*os.File, error) {
	return os.Open(path)
}
