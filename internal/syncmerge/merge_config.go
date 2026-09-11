//go:build spike

package syncmerge

import "github.com/acamarata/cascade/pkg/cascade"

// Sensitivity is the immutable per-record sensitivity tier asserted before
// serialization in every sync domain (R-21.223). local-only and restricted
// records never leave the local node.
type Sensitivity string

// The three sensitivity tiers this spike exercises.
const (
	SensitivityNormal     Sensitivity = "normal"
	SensitivityLocalOnly  Sensitivity = "local-only"
	SensitivityRestricted Sensitivity = "restricted"
)

// DomainName is one of the ratified SQLite domain names (R-21.223). The
// merge domains in this spike map onto this closed set; no "registry",
// "accounts" or "conversation" domain exists or may be created.
type DomainName string

// The two ratified domain names this spike's fixtures reference directly.
// Registry/account metadata lives under DomainConfig; conversation/memory
// records live under DomainContext.
const (
	DomainConfig  DomainName = "config"
	DomainContext DomainName = "context"
)

// ExclusionEntry journals one record excluded from serialization by its
// immutable sensitivity tier, so a later policy or classification change can
// rescan it (R-21.223). The cursor value names the point the transport
// advanced to, explicitly past the excluded record.
type ExclusionEntry struct {
	RecordID      string
	Domain        DomainName
	Sensitivity   Sensitivity
	PolicyVersion string
	CursorAfter   uint64
}

// ConfigRecord is one server-primary config record (R-21.223 domain 1).
// Ordering across two copies of the same RecordID is the server-assigned
// monotonic Revision, then the persisted hybrid logical clock (HLC), then
// the writer NodeID -- wall-clock time (WallClock) is carried for
// provenance only and is NEVER compared.
type ConfigRecord struct {
	RecordID    string
	Revision    uint64
	HLC         uint64
	NodeID      string
	WallClock   int64 // unix seconds, provenance only, never compared
	Value       string
	ValueHash   string
	Sensitivity Sensitivity
	Tombstone   bool
}

// RecordID satisfies the Sensitive constraint used by FilterSensitive.
func (r ConfigRecord) recordID() string { return r.RecordID }

// SensitivityTier satisfies the Sensitive constraint used by FilterSensitive.
func (r ConfigRecord) sensitivityTier() Sensitivity { return r.Sensitivity }

// ConfigJournalEntry records a losing local write during config merge,
// carrying both revisions and both content hashes so the loss is
// inspectable (R-21.223 domain 1).
type ConfigJournalEntry struct {
	RecordID        string
	WinningRevision uint64
	WinningHLC      uint64
	WinningNodeID   string
	WinningHash     string
	LosingRevision  uint64
	LosingHLC       uint64
	LosingNodeID    string
	LosingHash      string
}

// compareConfig returns >0 if a strictly outranks b under the R-21.223
// domain-1 ordering (revision, then HLC, then node id), 0 if they are
// identical under that ordering, and <0 otherwise. Wall-clock time never
// participates. This is a total order, so taking the max per record id is
// commutative, associative and idempotent by construction.
func compareConfig(a, b ConfigRecord) int {
	switch {
	case a.Revision != b.Revision:
		if a.Revision > b.Revision {
			return 1
		}
		return -1
	case a.HLC != b.HLC:
		if a.HLC > b.HLC {
			return 1
		}
		return -1
	case a.NodeID != b.NodeID:
		if a.NodeID > b.NodeID {
			return 1
		}
		return -1
	default:
		return 0
	}
}

// normaliseConfig returns a canonical copy of a config state map: this is
// what merge(A, A) must equal for the idempotence property, and what a
// single-sided input reduces to before merge (no-op today, since a fixture
// side is already keyed uniquely by RecordID, but named explicitly so the
// idempotence property has a stated target rather than comparing merge(A,A)
// to the arbitrary literal A).
func normaliseConfig(side map[string]ConfigRecord) map[string]ConfigRecord {
	out := make(map[string]ConfigRecord, len(side))
	for id, rec := range side {
		out[id] = rec
	}
	return out
}

// configMergeLWW merges two sides of the config domain by taking, per record
// id, the side that outranks the other under compareConfig. The losing
// local write is journaled with both revisions and both content hashes
// (R-21.223 domain 1). Deletes are ordinary values under this ordering (a
// Tombstone=true record competes exactly like any other value; config has
// no dominant-tombstone rule, unlike the memory domain).
func configMergeLWW(sideA, sideB map[string]ConfigRecord) (map[string]ConfigRecord, []ConfigJournalEntry) {
	merged := make(map[string]ConfigRecord, len(sideA)+len(sideB))
	var journal []ConfigJournalEntry

	ids := make(map[string]struct{}, len(sideA)+len(sideB))
	for id := range sideA {
		ids[id] = struct{}{}
	}
	for id := range sideB {
		ids[id] = struct{}{}
	}

	for id := range ids {
		a, aok := sideA[id]
		b, bok := sideB[id]
		switch {
		case aok && !bok:
			merged[id] = a
		case !aok && bok:
			merged[id] = b
		default:
			cmp := compareConfig(a, b)
			switch {
			case cmp >= 0:
				merged[id] = a
				if cmp > 0 {
					journal = append(journal, ConfigJournalEntry{
						RecordID:        id,
						WinningRevision: a.Revision, WinningHLC: a.HLC, WinningNodeID: a.NodeID, WinningHash: a.ValueHash,
						LosingRevision: b.Revision, LosingHLC: b.HLC, LosingNodeID: b.NodeID, LosingHash: b.ValueHash,
					})
				}
			default:
				merged[id] = b
				journal = append(journal, ConfigJournalEntry{
					RecordID:        id,
					WinningRevision: b.Revision, WinningHLC: b.HLC, WinningNodeID: b.NodeID, WinningHash: b.ValueHash,
					LosingRevision: a.Revision, LosingHLC: a.HLC, LosingNodeID: a.NodeID, LosingHash: a.ValueHash,
				})
			}
		}
	}
	return merged, journal
}

// ErrPeerCursorTooOld is the domain-2 sentinel for a peer resync request
// whose cursor predates the oldest retained tombstone (R-21.223): tombstones
// are never pruned in P1, so this refuses rather than resurrecting deleted
// records. It wraps the frozen KindConflict, since the request conflicts
// with the retention policy rather than naming a missing resource.
var ErrPeerCursorTooOld = cascade.New(cascade.KindConflict, "peer cursor predates oldest retained tombstone: full resync required")

// ErrPhaseStateDiverged is the domain-3 sentinel for a non-fast-forward or
// conflicting phase-state merge (R-21.223): git carriage is fetch and
// fast-forward only, so a divergence is refused rather than engine-merged.
// It wraps the frozen KindConflict.
var ErrPhaseStateDiverged = cascade.New(cascade.KindConflict, "phase state diverged: fetch+fast-forward only, refusing to merge")

// ErrBlobDigestMismatch is the domain-4 sentinel for a staged blob whose
// recomputed BLAKE3 digest does not equal its declared content address
// (R-21.223): the staged bytes are discarded rather than admitted under a
// wrong address. It wraps the frozen KindIntegrity.
var ErrBlobDigestMismatch = cascade.New(cascade.KindIntegrity, "staged blob digest does not match declared content address")

// sensitive is the structural constraint FilterSensitive requires: any
// record type that can report its own id and immutable sensitivity tier.
// ConfigRecord, MemoryRecord and Blob all satisfy it.
type sensitive interface {
	recordID() string
	sensitivityTier() Sensitivity
}

// FilterSensitive filters records by their immutable sensitivity metadata
// BEFORE serialization (R-21.223): local-only and restricted records never
// serialize, in any domain. Each exclusion is journaled by record id plus
// the policy version, and cursorAfter is the explicit point the transport
// cursor advances to past the excluded id, so a later policy change can
// rescan it.
func FilterSensitive[T sensitive](domain DomainName, records []T, policyVersion string, cursorAfter uint64) (kept []T, journal []ExclusionEntry) {
	for _, r := range records {
		switch r.sensitivityTier() {
		case SensitivityLocalOnly, SensitivityRestricted:
			journal = append(journal, ExclusionEntry{
				RecordID:      r.recordID(),
				Domain:        domain,
				Sensitivity:   r.sensitivityTier(),
				PolicyVersion: policyVersion,
				CursorAfter:   cursorAfter,
			})
		case SensitivityNormal:
			kept = append(kept, r)
		}
	}
	return kept, journal
}
