// Purpose: the ElevationKeystore contract every platform backend
//
//	implements, plus the typed errors and storage-tier enum the rest of
//	internal/elevation and cmd/cascade/elevate_helper.go build on.
//
// Inputs: none at this file's level — platform backends (keystore_darwin.go,
//
//	keystore_linux.go, keystore_windows.go) construct the concrete types.
//
// Outputs: the ElevationKeystore interface and its error vocabulary.
// Constraints: this package is the hardware root of trust for the §D-24
//
//	elevation flow. It must never import internal/secrets or any vault
//	package (D-24; enforced by the depguard rule this ticket adds to
//	.golangci.yml). Every method fails closed: an implementation that
//	cannot prove a safe outcome returns an error, never a zero-value
//	success (R-14.163).
//
// SPORT: internal/elevation ElevationKeystore/ADDED (P1-E04-W1-S07-T6).

package elevation

import "github.com/acamarata/cascade/pkg/cascade"

// StorageTier names which hardware or OS facility a keystore selected to
// hold the private key, surfaced by `cascade doctor` per this ticket's
// acceptance criteria. It is informational only — no code branches on the
// tier string beyond doctor-surface reporting and test assertions.
type StorageTier string

// TierSecureEnclave and TierTPM are deliberately NOT declared here. This
// ticket's Art.12 finding is that neither is reachable: Apple's Secure
// Enclave cannot hold an Ed25519 signing key (see keystore_darwin.go's doc
// comment), and TPM2/PKCS#11 detection on linux has no pinned module-path
// convention in this ticket's spec (see keystore_linux.go's doc comment).
// A Go dead-code gate (internal/build) fails on any exported internal/
// symbol nothing references, so declaring tiers this ticket cannot
// actually produce would be undead code, not documentation — the gap is
// recorded in prose in those two files' doc comments and in the ticket
// journal instead.
const (
	// TierOSKeychain means the key is stored in the OS-provided keychain
	// (macOS Keychain), access-control gated behind local authentication.
	TierOSKeychain StorageTier = "os-keychain"
	// TierOSKeyring means the key is stored in the OS session keyring
	// (linux secret-service/keyctl fallback when no TPM is present).
	TierOSKeyring StorageTier = "os-keyring"
	// TierWindowsTier2 marks the Windows tier-2 refusal backend: no key
	// storage exists because every elevated operation is refused.
	TierWindowsTier2 StorageTier = "windows-tier2"
	// TierUnavailable means no backing store could be selected on this
	// host (e.g. headless CI with no keychain daemon and CI_SKIP_BIOMETRICS
	// unset).
	TierUnavailable StorageTier = "unavailable"
)

// ElevationKeystore is the per-device hardware root of trust for the
// elevation flow: one Ed25519 keypair, generated once, whose private half
// never leaves this interface's implementation and whose Sign method
// performs local authentication and signing ATOMICALLY in a single call —
// no auth token is ever cached or reused across calls (hard requirement of
// this ticket's contract).
//
// Every method fails closed (R-14.163): missing enrollment, unavailable
// hardware, failed authentication, or any other condition an
// implementation cannot positively rule out MUST return a typed error,
// never a value that reads as success to a careless caller.
//
// The type name intentionally stutters with the package name: it is this
// ticket's contract-mandated name (04-PEWS-PLAN-W1-W3.md Epic D S-07.T6
// task 1), chosen so every call site reads unambiguously as THE elevation
// keystore, not an arbitrary Keystore some other package might also
// define.
//
//nolint:revive // contract-mandated name, see doc comment above
type ElevationKeystore interface {
	// GenerateKey creates a new Ed25519 keypair in this keystore's backing
	// store, if one does not already exist. Calling GenerateKey when a key
	// already exists is a no-op success (idempotent), never a silent
	// regeneration — regenerating would orphan an already-enrolled trust
	// record for no caller-visible reason.
	GenerateKey() error
	// PubKeyB64 returns the base64-standard-encoded Ed25519 public key.
	// Reading the public key never requires local authentication — only
	// Sign does. Returns ErrHelperNotEnrolled if GenerateKey has not
	// succeeded yet.
	PubKeyB64() (string, error)
	// Sign performs local authentication (LocalAuthentication on darwin,
	// PAM on linux) and, ONLY on success, signs payload with the Ed25519
	// private key in the same call. On authentication failure it returns
	// ErrAuthFailed and signs nothing. The private key material and any
	// authentication result are never retained past this call returning.
	Sign(payload []byte) ([]byte, error)
	// IsAvailable reports whether this keystore's backing hardware/OS
	// facility can be used at all on this host (e.g. a keychain daemon is
	// reachable, PAM is configured). It does not report whether a key has
	// been generated yet — callers check that via PubKeyB64/GenerateKey.
	IsAvailable() bool
	// Tier reports which storage tier this keystore selected, for the
	// `cascade doctor` surface and tests. Unspecified before GenerateKey
	// has run at least once; implementations return TierUnavailable until
	// then.
	Tier() StorageTier
}

// ErrHelperNotEnrolled reports that no key has been generated/enrolled yet
// (KindNotFound: the record a caller asked about does not exist).
func ErrHelperNotEnrolled() error {
	return cascade.New(cascade.KindNotFound, "elevation: helper has no enrolled key yet (run `cascade elevate-helper --enroll` first)")
}

// ErrAuthFailed reports that local authentication (LocalAuthentication /
// PAM) did not succeed, so no signature was produced. KindPermissionDenied:
// the caller lacks the local-presence proof this operation requires, and
// this layer offers no further elevation path of its own.
func ErrAuthFailed(cause error) error {
	if cause == nil {
		return cascade.New(cascade.KindPermissionDenied, "elevation: local authentication failed")
	}
	return cascade.Wrap(cascade.KindPermissionDenied, cause, "elevation: local authentication failed")
}

// ErrAlreadyEnrolled reports a TOFU conflict: an enrollment record already
// exists and the caller did not present a valid attestation from the
// currently-enrolled key to authorize replacing it. KindConflict: a
// duplicate/competing write against a uniquely-keyed resource.
func ErrAlreadyEnrolled() error {
	return cascade.New(cascade.KindConflict, "elevation: a trust record is already enrolled for a different key (TOFU) — re-enrollment requires a valid attestation from the enrolled key")
}

// ErrWindowsTier2 reports that Windows is a tier-2 platform for elevation:
// every ElevationKeystore operation refuses. KindUnsupported: the operation
// is recognized but not implemented on this platform/configuration, per
// 06-FORGE-SPEC §2 and §D-24.
func ErrWindowsTier2() error {
	return cascade.New(cascade.KindUnsupported, "elevation: elevated verbs are refused on Windows (tier-2 platform per §D-24); no local elevation helper is implemented")
}

// ErrKeystoreUnavailable reports that this host has no usable backing store
// for the keystore (no keychain daemon, no PAM stack, etc.).
// KindUnavailable: the caller may retry once the dependency is reachable,
// but nothing here signs on this host today.
func ErrKeystoreUnavailable(cause error) error {
	if cause == nil {
		return cascade.New(cascade.KindUnavailable, "elevation: no hardware/OS keystore is available on this host")
	}
	return cascade.Wrap(cascade.KindUnavailable, cause, "elevation: no hardware/OS keystore is available on this host")
}

// ErrNoInput reports that CASCADE_NO_INPUT=1 is set and the requested
// operation would have prompted for local authentication.
// KindPermissionDenied: automation explicitly declined to be prompted, so
// this call refuses before touching any auth API (§5.8, rule-23 parity).
func ErrNoInput() error {
	return cascade.New(cascade.KindPermissionDenied, "elevation: CASCADE_NO_INPUT=1 is set; refusing to prompt for local authentication")
}
