// Purpose: the trust_tier enum, its ordered rank, and the placement/sync
//
//	gate every later Epic Q consumer (S-37.T1 placement, S-37.T2 dispatch,
//	S-37.T3 re-queue, S-38.T2 sync eligibility) reads.
//
// Inputs: a raw string (from an enroll payload or a stored record).
// Outputs: a validated Tier, or a typed fail-closed error for anything
//
//	unknown or unset.
//
// Constraints: R-21.220 — the tier set is an ORDERED rank, controller=2 >
//
//	worker-trusted=1 > paired-device=0, never an unordered set; every gate
//	is `rank(tier) >= rank(worker-trusted)`. Unknown/unset values are
//	rejected at validation, never defaulted (fail-closed).
//
// SPORT: internal/nodes Tier/ADDED (P1-E17-W4-S36-T1).

package nodes

import "github.com/acamarata/cascade/pkg/cascade"

// Tier is the normative trust_tier enum (06 §5.22): controller |
// worker-trusted | paired-device. It is a defined string type so a typo is
// a compile-time mismatch, never a silently-wrong bare string threaded
// through a device record.
type Tier string

// The closed three-value enumeration. Declaration order here is NOT the
// rank order (Rank below is the source of truth for ordering); this order
// only matches the contract's own listing.
const (
	// TierController: the local controller machine. Highest rank. May
	// carry local-only work and every restricted domain.
	TierController Tier = "controller"
	// TierWorkerTrusted: a remote worker enrolled with elevated trust.
	// Middle rank. Satisfies every restricted gate but never local-only
	// placement (local-only is controller-machine-only by definition).
	TierWorkerTrusted Tier = "worker-trusted"
	// TierPairedDevice: a paired device (phone, companion machine).
	// Lowest rank. May carry bridge approvals (W/S-48.T4) but satisfies
	// no node-dispatch and no sync gate a worker or controller would.
	TierPairedDevice Tier = "paired-device"
)

// Rank returns t's ordinal per R-21.220: controller=2 > worker-trusted=1 >
// paired-device=0. ok is false for any value outside the closed three,
// including the zero value "" — callers MUST check ok and fail closed
// rather than treating a zero Rank as paired-device's rank, which happens
// to also be zero.
func Rank(t Tier) (rank int, ok bool) {
	switch t {
	case TierController:
		return 2, true
	case TierWorkerTrusted:
		return 1, true
	case TierPairedDevice:
		return 0, true
	default:
		return -1, false
	}
}

// Gate names one of the two sensitivity gates every consumer checks a
// tier's rank against (06 §5.22).
type Gate string

const (
	// GateLocalOnly is local-only work — controller machine only. No
	// tier's rank satisfies this gate except TierController itself; this
	// gate is NOT `rank >= X`, it is an exact identity check, because
	// "local" names one machine, not a rank threshold.
	GateLocalOnly Gate = "local-only"
	// GateRestricted is restricted work — any tier whose rank is at least
	// worker-trusted's.
	GateRestricted Gate = "restricted"
)

// Satisfies reports whether t clears gate, per R-21.220's restated rule:
// every consumer gate reads `rank(tier) >= rank(worker-trusted)` for
// GateRestricted; GateLocalOnly is the one exception (identity check
// against TierController, since "local" means the controller machine
// itself, not a rank threshold). An unrecognized Tier or Gate never
// satisfies anything (fail closed).
func Satisfies(t Tier, gate Gate) bool {
	rank, ok := Rank(t)
	if !ok {
		return false
	}
	switch gate {
	case GateLocalOnly:
		return t == TierController
	case GateRestricted:
		workerRank, _ := Rank(TierWorkerTrusted)
		return rank >= workerRank
	default:
		return false
	}
}

// ValidateTier fail-closed-validates a raw tier string from an untrusted
// source (an enroll payload, a stored record read back). An empty string,
// an unrecognized value, or (per R-21.220) TierPairedDevice on the enroll
// path all return a typed error; there is no permissive default anywhere
// in this function.
//
// allowPaired controls whether TierPairedDevice is an acceptable result:
// the enroll path (enroll.go) always calls this with allowPaired=false,
// because paired-device is not assignable there (the W/S-48.T1 bridge
// binding is a distinct record kind). Any other reader of a persisted
// trust_tier value may pass allowPaired=true.
func ValidateTier(raw string, allowPaired bool) (Tier, error) {
	if raw == "" {
		return "", cascade.New(cascade.KindInvalidInput,
			"nodes: trust_tier is required and is never defaulted")
	}
	t := Tier(raw)
	if _, ok := Rank(t); !ok {
		return "", cascade.Newf(cascade.KindInvalidInput,
			"nodes: unrecognized trust_tier %q (must be one of controller, worker-trusted, paired-device)", raw)
	}
	if t == TierPairedDevice && !allowPaired {
		return "", cascade.New(cascade.KindInvalidInput,
			"nodes: paired-device is not assignable on the enroll path (it is a distinct bridge-binding record kind, W/S-48.T1)")
	}
	return t, nil
}
