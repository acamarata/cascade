//go:build darwin

// Package runtime (daemonless_darwin.go): Purpose: darwin-specific documentation seam for §D-24 daemonless
//
//	elevation preconditions. The actual query (S-07.T6's ElevationTrustStore
//	for enrollment + ElevationKeystore.IsAvailable for LocalAuthentication)
//	lives in internal/elevation, which cannot be imported from here — see
//	ElevationPrecondition's doc comment (daemonless.go) for the cycle.
//	cmd/cascade (composition root) wires the real check.
//
// SPORT: internal/runtime EmbeddedRuntime/CHANGED (darwin).
package runtime

// PlatformElevationUnavailableReason names the darwin-specific reason a
// composition-root ElevationPrecondition may report
// authenticatorAvailable=false even on a successful build: LocalAuthentication
// requires cgo, and internal/elevation/keystore_darwin_nocgo.go's
// cgo-free build always reports IsAvailable()=false. This is the standing
// finding R-14.172/R-14.174 flag as the case real release binaries hit.
func PlatformElevationUnavailableReason() string {
	return "darwin: LocalAuthentication requires the cgo-enabled keystore backend; a cgo-free build always reports the authenticator unavailable"
}
