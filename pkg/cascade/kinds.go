package cascade

// Kind identifies one member of the taxonomy's closed error-kind enumeration
// (T0 ruling R-14.3). The set is FROZEN at exactly 14 members; adding,
// removing, or renaming a kind requires a T0 amendment to R-14.3 — it is not
// a build-time decision. capability-denied and quota-exhausted are distinct
// by design: a capability a tier does not sell must never be retried or
// rotated as if it were a spent quota (PCI provider-capability-flags,
// 2026-09-01).
//
// The zero value is intentionally invalid (not a member of the taxonomy) so
// a forgotten Kind field reads as a bug rather than silently meaning
// KindNotFound. Callers that need to test membership use Valid.
type Kind uint8

// The frozen 14-kind enumeration, in the order R-14.3 lists them. Declaration
// order here is the single source AllKinds walks; codes.go's tables are keyed
// off these same constants so the three wire tables (CLI exit, JSON-RPC,
// plugin RPC) stay total and non-overlapping by construction.
const (
	_ Kind = iota // 0 is deliberately not a valid Kind

	// KindNotFound reports that a named resource does not exist.
	KindNotFound
	// KindInvalidInput reports that caller-supplied input failed validation.
	KindInvalidInput
	// KindConflict reports a state conflict (e.g. optimistic-concurrency
	// mismatch, duplicate creation of a uniquely-keyed resource).
	KindConflict
	// KindUnavailable reports that a dependency is temporarily unreachable
	// and the caller may retry.
	KindUnavailable
	// KindTimeout reports that an operation exceeded its deadline.
	KindTimeout
	// KindCanceled reports that an operation was canceled by its caller
	// (context cancellation, SIGINT).
	KindCanceled
	// KindPermissionDenied reports that the caller lacks the rights an
	// operation requires, with no elevation path being offered.
	KindPermissionDenied
	// KindElevationRequired reports that the operation needs an elevation
	// flow (e.g. a fresh attestation) before it can proceed.
	KindElevationRequired
	// KindPolicyDenied reports that policy evaluation (internal/policy)
	// refused the operation.
	KindPolicyDenied
	// KindCapabilityDenied reports that the active tier does not sell the
	// requested capability at all. Distinct from KindQuotaExhausted: never
	// retry or rotate accounts in response to this kind.
	KindCapabilityDenied
	// KindQuotaExhausted reports that a purchased capability's quota is
	// spent for the current window; retry/rotation is a valid response.
	KindQuotaExhausted
	// KindUnsupported reports that the operation is recognized but not
	// implemented on the current platform, build, or configuration.
	KindUnsupported
	// KindIntegrity reports that a verification step (checksum, signature,
	// schema) failed.
	KindIntegrity
	// KindInternal reports an unclassified internal failure. It is the
	// fallback kind for wire mapping when no more specific kind applies.
	KindInternal
)

// kindNames holds the display string for each valid Kind, indexed by Kind
// value. Index 0 is the invalid zero value's placeholder.
var kindNames = [...]string{
	"",
	"not-found",
	"invalid-input",
	"conflict",
	"unavailable",
	"timeout",
	"canceled",
	"permission-denied",
	"elevation-required",
	"policy-denied",
	"capability-denied",
	"quota-exhausted",
	"unsupported",
	"integrity",
	"internal",
}

// String returns the kind's stable lowercase-hyphenated name, e.g.
// "not-found" or "elevation-required". These strings are part of the wire
// contract (they appear in JSON-RPC error data and CLI diagnostics) and must
// never change once a kind ships.
func (k Kind) String() string {
	if !k.Valid() {
		return "invalid-kind"
	}
	return kindNames[k]
}

// Valid reports whether k is a member of the frozen R-14.3 taxonomy
// enumeration. The zero value and any value beyond KindInternal are invalid.
func (k Kind) Valid() bool {
	return k >= KindNotFound && k <= KindInternal
}

// AllKinds returns the closed set of taxonomy kinds in R-14.3's declaration
// order. Tests use it to assert the enumeration stays exactly 14 members
// (zero additions, zero omissions) and that every wire table is total over
// this same set.
func AllKinds() []Kind {
	return []Kind{
		KindNotFound,
		KindInvalidInput,
		KindConflict,
		KindUnavailable,
		KindTimeout,
		KindCanceled,
		KindPermissionDenied,
		KindElevationRequired,
		KindPolicyDenied,
		KindCapabilityDenied,
		KindQuotaExhausted,
		KindUnsupported,
		KindIntegrity,
		KindInternal,
	}
}
