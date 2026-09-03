//go:build windows

// Package runtime (daemonless_windows.go): Purpose: windows-specific documentation seam for §D-24 daemonless
//   elevation preconditions, mirroring daemonless_darwin.go /
//   daemonless_linux.go. Windows has no daemon at all (tier-2,
//   internal/daemon/lifecycle_windows.go already refuses every
//   service-install/start/stop/restart verb with a typed KindUnsupported
//   error — this ticket does not duplicate that refusal here). This file
//   only documents why the elevation authenticator is unconditionally
//   unavailable on Windows: internal/elevation/keystore_windows.go's
//   IsAvailable() always returns false (no Windows elevation backend
//   exists at v2.0.0).
// Inputs: none.
// Outputs: PlatformElevationUnavailableReason's string.
// Constraints: ProbeDaemonless (daemonless.go) needs no windows override —
//   Windows never has a daemon socket to find, so probeSocket's ordinary
//   "no socket file" branch already reports Embedded=true unconditionally,
//   for free, with no platform-specific code.
// SPORT: internal/runtime EmbeddedRuntime/CHANGED (windows).
package runtime

// PlatformElevationUnavailableReason names why every elevated verb refuses
// on Windows: no elevation backend exists at all (tier-2, §D-24), so
// authenticatorAvailable is unconditionally false regardless of
// helperEnrolled.
func PlatformElevationUnavailableReason() string {
	return "windows: no elevation backend exists (tier-2 per §D-24); every elevated verb refuses unconditionally"
}
