// Package fixturehelper (crosspkg/helper.go) is the R-14.137 cross-package
// proof's helper half: it writes to os.Stdout directly, and lives OUTSIDE
// cmd/, so a cmd/-only scan (the old forbidigo scope) would never see it.
// This gate catches it because it scans every non-exempt package, not only
// cmd/ — see crosspkg/caller.go for the call site this defeats a per-file,
// single-directory scan.
package fixturehelper

import "os"

func WriteBanner(msg string) {
	os.Stdout.Write([]byte(msg))
}
