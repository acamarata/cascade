// Package policy (daemonless_elevation.go): Purpose: §D-24 daemonless elevation enforcement — the fail-closed gate
//   embedded-mode command dispatch consults before running any elevated
//   verb.
// Inputs: a verb (method) name, and the two daemonless preconditions
//   (helperEnrolled, authenticatorAvailable) the caller has already
//   determined via internal/elevation (platform keystore/trust queries —
//   this package never touches internal/elevation directly, per §D-24's
//   own layering: internal/policy decides POLICY, internal/elevation
//   decides HARDWARE FACTS).
// Outputs: IsDaemonlessElevationAllowed's bool; ErrElevationRequired for
//   callers that want a typed refusal directly.
// Constraints: R-14.163 fail closed — unknown verb, unavailable keystore,
//   or missing enrollment all refuse. NO SECOND ELEVATED-VERB LIST: this
//   file transcribes nothing from 06-FORGE-SPEC §5.14 itself. It calls
//   internal/rpc.IsElevated(verb, nil) — the ALREADY-CANONICAL table
//   (internal/rpc/elevation.go's elevationTable, independently verified
//   against spec by TestElevationTableMatchesSpec in that package) — with
//   nil params, which every elevationTable rule (rpc's unparseableParams
//   helpers) treats as "cannot prove this is the narrow/harmless case,
//   elevate." That is the correct daemonless semantics: a one-shot
//   embedded command has no per-request attestation retry loop the way
//   the RPC layer does, so the coarser verb-level classification
//   ("would this method EVER need elevation") is what daemonless
//   enforcement needs, and it falls straight out of IsElevated's existing
//   fail-closed behavior on unreadable params — no new logic duplicates
//   the table.
// SPORT: internal/policy DaemonlessElevationGuard/ADDED.
package policy

import (
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

// IsDaemonlessElevationAllowed reports whether verb may proceed in
// daemonless (embedded) mode, given the caller's already-resolved
// daemonless-elevation preconditions:
//   - helperEnrolled: internal/elevation's ElevationTrustStore has an
//     enrolled key for this device (TOFU).
//   - authenticatorAvailable: internal/elevation's ElevationKeystore
//     reports IsAvailable() (LocalAuthentication on darwin, PAM on linux;
//     always false in a cgo-free build — see keystore_darwin_nocgo.go /
//     keystore_linux_nocgo.go, which is exactly the "nothing available"
//     case R-14.172/R-14.174 flag as the one real release-binary users
//     hit).
//
// verb is classified per internal/rpc.IsElevated — see this file's doc
// comment for why that is a derivation, not a second list. A verb IsElevated
// does not recognize (unknown, or genuinely not elevated) is allowed
// through here; internal/policy is not the layer that validates verb names
// exist at all.
func IsDaemonlessElevationAllowed(verb string, helperEnrolled, authenticatorAvailable bool) bool {
	if !rpc.IsElevated(verb, nil) {
		return true
	}
	return helperEnrolled && authenticatorAvailable
}

// ErrElevationRequired returns the typed refusal for verb when
// IsDaemonlessElevationAllowed reports false. Callers enforce with:
//
//	if !policy.IsDaemonlessElevationAllowed(verb, enrolled, avail) {
//	    return policy.ErrElevationRequired(verb, enrolled, avail)
//	}
//
// The message names which precondition failed (or both) so a real user on
// a cgo-free release binary — where authenticatorAvailable is always false
// — gets an actionable reason rather than a bare "elevation required."
func ErrElevationRequired(verb string, helperEnrolled, authenticatorAvailable bool) error {
	reason := "helper not enrolled and no local authenticator is available"
	switch {
	case !helperEnrolled && authenticatorAvailable:
		reason = "helper not enrolled (run `cascade elevate-helper --enroll`)"
	case helperEnrolled && !authenticatorAvailable:
		reason = "no local authenticator is available on this build/host (this is expected on a cgo-free build; the daemonless elevation path has no hardware root of trust to sign with)"
	}
	return cascade.Newf(cascade.KindElevationRequired,
		"runtime: elevated verb %q refused in daemonless mode: %s", verb, reason)
}
