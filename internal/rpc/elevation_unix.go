//go:build !windows

package rpc

// platformElevationRefusal always returns nil on POSIX platforms: the real
// ELEVATION_REQUIRED + attestation flow runs normally. Windows tier-2
// (elevation_windows.go) overrides this to always refuse, per this
// ticket's contract ("Windows tier-2 (§2): elevated verbs always return
// ELEVATION_REQUIRED with a message directing to a POSIX platform").
func platformElevationRefusal() *ErrorObject {
	return nil
}
