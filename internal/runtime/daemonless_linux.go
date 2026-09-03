//go:build linux

// Package runtime (daemonless_linux.go): Purpose: linux-specific documentation seam for §D-24 daemonless
//   elevation preconditions — mirrors daemonless_darwin.go. The real
//   enrolled-helper + PAM-availability query lives in internal/elevation
//   (cannot be imported here, see ElevationPrecondition's doc comment in
//   daemonless.go); cmd/cascade wires it.
// SPORT: internal/runtime EmbeddedRuntime/CHANGED (linux).
package runtime

// PlatformElevationUnavailableReason names the linux-specific reason a
// composition-root ElevationPrecondition may report
// authenticatorAvailable=false: no PAM stack configured, or a cgo-free
// build (internal/elevation/keystore_linux_nocgo.go always reports
// IsAvailable()=false).
func PlatformElevationUnavailableReason() string {
	return "linux: PAM is not configured, or this is a cgo-free build (the cgo-free keystore backend always reports the authenticator unavailable)"
}
