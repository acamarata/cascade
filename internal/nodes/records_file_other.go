//go:build !windows

// Package nodes (records_file_other.go): Purpose: the non-windows classifier behind records_file_retry.go. POSIX
//
//	opens never conflict with a rename, so no record file error is
//	transient here and every error returns on the first attempt.
//
// SPORT: internal/nodes records file backend (CHG): windows sharing retry.
package nodes

import "os"

// isTransientShareError is always false off Windows.
func isTransientShareError(error) bool { return false }

// readRecordFile is os.ReadFile: POSIX has no share modes to widen.
func readRecordFile(path string) ([]byte, error) { return os.ReadFile(path) }

// foldLockKey is the identity: POSIX paths are case-sensitive.
func foldLockKey(key string) string { return key }
