package sync

import "github.com/acamarata/cascade/internal/storage"

// Purpose (this file): which merge strategy each sync domain gets, and the
//
//	rule that a domain nobody mapped never syncs at all.
//
// FAIL-CLOSED BY OMISSION (R-16.56). A lookup miss resolves to
//
//	StrategyNone — "this never leaves the device" — and is not an error.
//	That distinction is the whole design: an error would make a new domain
//	break sync until somebody mapped it, so the pressure would be to add a
//	permissive default; a silent no-sync makes a new domain safe by
//	default and visible the moment somebody expects it to replicate. There
//	is no fallthrough case that promotes an unmapped domain, and adding one
//	would be the bug this file exists to prevent.
//
// THE TABLE IS DERIVED, NOT RESTATED. Every strategy here is resolved from
//
//	the S-38.T1 domain registry's own Class, so a domain's sync class is
//	stated once. A second hand-written table would drift, and the way it
//	would drift is by keeping a strategy for a domain whose class had
//	changed underneath it.
//
// Inputs: a domain and subkind.
// Outputs: the strategy that merges it.
// SPORT: internal/sync strategy registry (ADD) — P1-E17-W4-S38-T2.

// StrategyName is one merge strategy.
type StrategyName string

const (
	// StrategyNone never merges because the domain never syncs.
	StrategyNone StrategyName = "none"
	// StrategyServerPrimaryLWW keeps the server's write and journals the
	// local one that lost.
	StrategyServerPrimaryLWW StrategyName = "server-primary-lww"
	// StrategyAppendTombstone unions records and lets a tombstone
	// dominate a concurrent update.
	StrategyAppendTombstone StrategyName = "append-merge-tombstones"
	// StrategyGitCarried delegates carriage to git, fast-forward only.
	StrategyGitCarried StrategyName = "git-carried"
	// StrategyMetadataOnly is server-primary over metadata, with the
	// credential material structurally absent.
	StrategyMetadataOnly StrategyName = "metadata-only-server-primary"
	// StrategyContentAddressUnion unions admitted blobs by content
	// address.
	StrategyContentAddressUnion StrategyName = "content-address-union"
)

// StrategyFor returns the merge strategy for one domain and subkind.
//
// A subkind the registry does not carry returns StrategyNone and false —
// never a default merge (R-16.56). The bool is returned alongside because
// "mapped to none" and "not mapped" are both no-sync and an operator
// debugging a domain that will not replicate needs to know which.
func StrategyFor(domain storage.DomainID, subkind string) (StrategyName, bool) {
	dc, ok := Lookup(domain, subkind)
	if !ok {
		return StrategyNone, false
	}
	return strategyForClass(dc), true
}

// strategyForClass maps a registered domain onto its merge.
//
// Keyed on the SUBKIND first where the subkind is what decides: "blobs"
// and "phase-state" are both ClassSynced, and they merge nothing like each
// other — one is a content-addressed set union and the other is a git
// fast-forward. Class alone cannot tell them apart, and a switch that
// pretended it could would give phase state a set union.
func strategyForClass(dc DomainClass) StrategyName {
	switch dc.Subkind {
	case subkindBlobs:
		return StrategyContentAddressUnion
	case subkindPhaseState:
		return StrategyGitCarried
	case subkindRegistry, subkindAccounts:
		return StrategyMetadataOnly
	}
	switch dc.Class {
	case ClassSynced, ClassSyncedAppend:
		if dc.Domain == storage.DomainMemory || dc.Domain == storage.DomainContext {
			return StrategyAppendTombstone
		}
		return StrategyServerPrimaryLWW
	case ClassServerPrimary:
		return StrategyServerPrimaryLWW
	case ClassLocalOnly:
		return StrategyNone
	default:
		// Unreachable for a registered class, and fail-closed anyway: a
		// fifth class added without visiting this switch gets no merge
		// rather than an arbitrary one.
		return StrategyNone
	}
}

// The subkinds whose merge is decided by the subkind rather than by the
// class. Named constants so the registry and this file cannot disagree
// about a string.
const (
	subkindBlobs      = "blobs"
	subkindPhaseState = "phase-state"
	subkindRegistry   = "registry"
	subkindAccounts   = "accounts"
)
