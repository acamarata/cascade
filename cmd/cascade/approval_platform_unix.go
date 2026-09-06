//go:build !windows

// Purpose: the POSIX half of the approval CLI's platform gate. On a
//
//	platform where an elevation flow exists, the elevated verbs proceed
//	and the daemon's own elevation middleware decides.
//
// SPORT: cli/approval-standing/ADD (P1-E09-W2-S18-T6).
package main

// refuseElevatedOnUnsupportedPlatform returns nil on POSIX: nothing is
// refused locally, and the elevation decision belongs to the daemon.
func refuseElevatedOnUnsupportedPlatform(string) error { return nil }
