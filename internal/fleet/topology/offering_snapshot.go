// Purpose: R-21.101's immutable, versioned OfferingSnapshot Lane persists,
//
//	and R-21.72's ModelIdentity{canonical_id, family} -- both populated
//	ONLY in providers/<vendor>/ discovery (this package parses no vendor
//	or model name, R-21.23).
//
// Inputs: none at this layer. Outputs: none.
// Constraints: Lane.Snapshot() returns a COPY; the replace is package-
//
//	private and bumps version+1, never mutating a field in place. An
//	empty canonical_id is fail-closed as the SAME model (R-21.72).
//
// SPORT: fleet/topology/offering_snapshot/ADD (P1-E40-W9-S77-T1).

package topology

import (
	"encoding/json"

	"github.com/acamarata/cascade/pkg/cascade"
)

// ModelIdentity is R-21.72's provider-declared identity. Both fields are
// populated only in providers/<vendor>/ discovery. An empty CanonicalID is
// treated as the SAME model for exclude_producer_model and as the same
// provider family for the R-21.33 correlation rows -- fail-closed, never
// as a distinct model.
type ModelIdentity struct {
	CanonicalID string
	Family      string
}

// SameModel reports whether a and b identify the same model under
// R-21.72's fail-closed rule: an empty CanonicalID on either side counts
// as a match rather than as "unknown, therefore distinct".
func (a ModelIdentity) SameModel(b ModelIdentity) bool {
	if a.CanonicalID == "" || b.CanonicalID == "" {
		return true
	}
	return a.CanonicalID == b.CanonicalID
}

// SameFamily reports whether a and b belong to the same provider family
// under the same R-21.72 fail-closed rule: an empty Family on either side
// counts as a match.
func (a ModelIdentity) SameFamily(b ModelIdentity) bool {
	if a.Family == "" || b.Family == "" {
		return true
	}
	return a.Family == b.Family
}

// ProviderPolicy is the provider-declared policy flags a discovery pass
// attaches to an offering. Kept as an opaque string map rather than a
// fixed struct: this package never interprets vendor policy semantics, it
// only persists and versions whatever providers/<vendor>/ discovery
// declared (R-21.23).
type ProviderPolicy struct {
	Flags map[string]string
}

// clone returns a deep copy of p (its Flags map is never shared).
func (p ProviderPolicy) clone() ProviderPolicy {
	out := ProviderPolicy{Flags: make(map[string]string, len(p.Flags))}
	for k, v := range p.Flags {
		out.Flags[k] = v
	}
	return out
}

// OfferingSnapshot is R-21.101's immutable, versioned lane-capability
// snapshot. Ranking, health summaries and learned statistics read ONLY
// this persisted snapshot, never a live provider response.
type OfferingSnapshot struct {
	Modalities         []string
	ContextTokens      int
	MaxOutputTokens    int
	Tools              []string
	Efforts            []Effort
	InteractionClasses []InteractionClass
	ModelIdentity      ModelIdentity
	ProviderPolicy     ProviderPolicy
	Version            int
}

// clone returns a deep copy of s: every slice and the ProviderPolicy map
// is copied, never shared, so a caller mutating the returned value can
// never reach the Lane's own stored snapshot.
func (s OfferingSnapshot) clone() OfferingSnapshot {
	out := s
	out.Modalities = append([]string(nil), s.Modalities...)
	out.Tools = append([]string(nil), s.Tools...)
	out.Efforts = append([]Effort(nil), s.Efforts...)
	out.InteractionClasses = append([]InteractionClass(nil), s.InteractionClasses...)
	out.ProviderPolicy = s.ProviderPolicy.clone()
	return out
}

// Snapshot returns a COPY of l's OfferingSnapshot -- ranking, health and
// learned-statistics readers must call this rather than reaching into the
// field directly, so no reader can mutate the Lane's persisted state.
func (l Lane) Snapshot() OfferingSnapshot { return l.OfferingSnapshot.clone() }

// replaceSnapshot atomically replaces l's OfferingSnapshot with next,
// bumping the version to the prior version + 1. Package-private: reachable
// only from reconcile.go's single commit transaction, never from an
// external caller, so no field is ever mutated in place outside that one
// path.
func (l *Lane) replaceSnapshot(next OfferingSnapshot) {
	next.Version = l.OfferingSnapshot.Version + 1
	l.OfferingSnapshot = next.clone()
}

// offeringSnapshotWire is OfferingSnapshot's JSON storage body, excluding
// the two fields store.go/reconcile.go keep as their own columns
// (model_identity_*, offering_snapshot_version) for query convenience.
type offeringSnapshotWire struct {
	Modalities         []string           `json:"modalities"`
	ContextTokens      int                `json:"context_tokens"`
	MaxOutputTokens    int                `json:"max_output_tokens"`
	Tools              []string           `json:"tools"`
	Efforts            []Effort           `json:"efforts"`
	InteractionClasses []InteractionClass `json:"interaction_classes"`
	ProviderPolicy     ProviderPolicy     `json:"provider_policy"`
}

// nonNilJSON returns s unchanged, or an empty (non-nil) slice for a nil s,
// so json.Marshal always writes "[]" rather than "null".
func nonNilJSON(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// encodeOfferingSnapshot marshals s's JSON storage body (everything except
// model_identity and version, which reconcile.go's caller stores as their
// own columns).
func encodeOfferingSnapshot(s OfferingSnapshot) (string, error) {
	wire := offeringSnapshotWire{
		Modalities: nonNilJSON(s.Modalities), ContextTokens: s.ContextTokens, MaxOutputTokens: s.MaxOutputTokens,
		Tools: nonNilJSON(s.Tools), Efforts: s.Efforts, InteractionClasses: s.InteractionClasses, ProviderPolicy: s.ProviderPolicy,
	}
	b, err := json.Marshal(wire)
	if err != nil {
		return "", cascade.Wrap(cascade.KindInternal, err, "topology: encode offering_snapshot")
	}
	return string(b), nil
}

// decodeOfferingSnapshot unmarshals data into out's JSON-body fields; the
// caller fills ModelIdentity/Version from their own columns.
func decodeOfferingSnapshot(data string, out *OfferingSnapshot) error {
	var wire offeringSnapshotWire
	if err := json.Unmarshal([]byte(data), &wire); err != nil {
		return cascade.Wrap(cascade.KindIntegrity, err, "topology: decode offering_snapshot")
	}
	out.Modalities, out.ContextTokens, out.MaxOutputTokens = wire.Modalities, wire.ContextTokens, wire.MaxOutputTokens
	out.Tools, out.Efforts, out.InteractionClasses, out.ProviderPolicy = wire.Tools, wire.Efforts, wire.InteractionClasses, wire.ProviderPolicy
	return nil
}
