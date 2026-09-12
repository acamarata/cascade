// Purpose: the PRE-SERIALIZATION sensitivity filter (R-21.223 §G.2): every
//   record is checked against its OWN immutable sensitivity metadata
//   BEFORE serialization, on top of (never instead of) the domain-class
//   gate. local-only and restricted records never serialize, even inside
//   an otherwise-synced domain.
// Inputs: a Record (domain, subkind, id, its own immutable Tier, policy
//   version, payload) and the domain-class registry (domains.go).
// Outputs: Admit reports whether rec may be serialized; when it may not,
//   it also returns the Exclusion the caller journals via
//   CursorStore.AdvanceOverExclusion so the cursor still advances past it.
// Constraints: FAIL CLOSED — an unregistered domain, an unresolved tier,
//   or a domain whose Class is local-only all refuse. There is no
//   fallthrough that admits by default.
// SPORT: internal.sync.filter/ADDED (P1-E17-W4-S38-T1).

package sync

import (
	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/storage"
)

// Record is one syncable unit: a row from a mapped storage domain (per
// domains.go's registry) carrying its own immutable sensitivity Tier —
// never derived from the domain's default, since a domain's default tier
// describes the common case, not every row in it.
type Record struct {
	Domain        storage.DomainID
	Subkind       string
	ID            string
	Tier          egress.SensitivityTier
	PolicyVersion int
	Payload       []byte
}

// AdmitResult is Admit's verdict.
type AdmitResult struct {
	Admitted bool
	// Reason names why an excluded record was excluded, in the shape
	// cursor.go's Exclusion.Reason expects.
	Reason string
}

// Admit applies the pre-serialization filter to rec. Order of checks,
// fail-closed at every step: (1) the domain+subkind must be registered
// and its Class must not be local-only; (2) the record's own Tier,
// resolved, must not be local-only or restricted. A miss or a refusal at
// either step returns Admitted=false with a Reason a caller journals.
func Admit(rec Record) AdmitResult {
	dc, ok := Lookup(rec.Domain, rec.Subkind)
	if !ok {
		return AdmitResult{Admitted: false, Reason: "domain-unregistered"}
	}
	if dc.Class == ClassLocalOnly {
		return AdmitResult{Admitted: false, Reason: "domain-local-only"}
	}
	tier := rec.Tier.Resolve()
	if tier == egress.TierLocalOnly {
		return AdmitResult{Admitted: false, Reason: "sensitivity-local-only"}
	}
	if tier == egress.TierRestricted {
		return AdmitResult{Admitted: false, Reason: "sensitivity-restricted"}
	}
	return AdmitResult{Admitted: true}
}

// FilterBatch applies Admit to every record in recs, returning the
// admitted subset (in the same relative order) and the exclusions for
// every record that was refused, ready for the caller to journal via
// CursorStore.AdvanceOverExclusion.
func FilterBatch(recs []Record) (admitted []Record, excluded []Exclusion) {
	for _, rec := range recs {
		res := Admit(rec)
		if res.Admitted {
			admitted = append(admitted, rec)
			continue
		}
		excluded = append(excluded, Exclusion{RecordID: rec.ID, Reason: res.Reason, PolicyVersion: rec.PolicyVersion})
	}
	return admitted, excluded
}
