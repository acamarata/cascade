// Package buildinfo holds the ldflags-stamped build identity (§D-33) —
// version, commit, build date, and install channel — as importable Go
// symbols, so any internal package (not just cmd/cascade, which is package
// main and cannot be imported) can read the same build stamp.
//
// Purpose: the single ldflags-stamp source of truth (R-14.116, superseding
//
//	an earlier draft that also stamped package-main vars in
//	cmd/cascade/version.go). cmd/cascade/version.go READS this package and
//	declares no stamp vars of its own — R-14.108's "T1 only reads it" read
//	literally per R-14.116(b).
//
// Inputs:  package-level vars set at build time via `-ldflags -X`; unset in
//
//	a plain `go build` (dev builds print the documented dev defaults).
//
// Outputs: Version, Commit, Date, InstallChannel, and ResolvedInstallChannel()
//
//	for callers that need the §D-33-normalized channel.
//
// Constraints: no business logic, no I/O. This package owns the ldflags
//
//	injection points (.goreleaser.yaml is the only place these are set).
//	See docs/developer/release.md for the full ldflags target list.
//
// SPORT: internal/buildinfo — ldflags stamp source of truth (A-T6).
package buildinfo

// These are the ldflags injection points. .goreleaser.yaml sets them via
// e.g. `-X .../internal/buildinfo.Version=v2.0.0
// -X .../internal/buildinfo.Commit=<sha>
// -X .../internal/buildinfo.Date=<rfc3339>
// -X .../internal/buildinfo.InstallChannel=script`. Left unset, a plain
// `go build` yields the dev defaults below.
var (
	// Version is the artifact tag (§D-33: v2.Y.Z, distinct from the v0.Y.Z
	// Go module tag).
	Version = "dev"
	// Commit is the short git SHA the binary was built from.
	Commit = "none"
	// Date is the RFC3339 build timestamp.
	Date = "unknown"
	// InstallChannel is the §D-33 distribution channel this binary was
	// stamped with at build/package time. Empty or unrecognized values
	// resolve to "manual" via ResolvedInstallChannel.
	InstallChannel = ""
)

// ValidInstallChannels are the §D-33 channel values install tooling may
// stamp. Mirrors cmd/cascade/version.go's validInstallChannels (kept in
// sync by inspection; both sets are the fixed §D-33 vocabulary).
var ValidInstallChannels = map[string]bool{
	"script":       true,
	"brew":         true,
	"oci":          true,
	"node-managed": true,
	"manual":       true,
}

// ResolvedInstallChannel returns the stamped install channel, falling back
// to "manual" when unstamped or stamped with a value outside the §D-33 set.
func ResolvedInstallChannel() string {
	if ValidInstallChannels[InstallChannel] {
		return InstallChannel
	}
	return "manual"
}
