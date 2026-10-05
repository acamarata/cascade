// Purpose: the one canonical untrusted-provenance type
//   (contract:untrusted-provenance). Content either came from somewhere the
//   user established (trusted) or it did not (untrusted-source); text that
//   carries the untrusted tag is data and never an instruction.
//   internal/retrieval/corpus.TrustLevel aliases this type.
// Inputs: a provenance string from a corpus definition, a stored row or a
//   wire field.
// Outputs: a Provenance, and the fail-closed join of several.
// Constraints: exactly two members and no "unknown" third state. Parsing is
//   closed: "" and any unrecognised value resolve to
//   ProvenanceUntrustedSource (unrecognised values also return
//   KindInvalidInput). The stored and wire spelling is the string value
//   itself, so aliasing changed no persisted bytes.
// SPORT: pkg.provider.untrusted-provenance/ADD (contract:untrusted-provenance).

package provider

import "github.com/acamarata/cascade/pkg/cascade"

// Provenance is the trust classification of a piece of content: whether
// the user vouched for its origin. The zero value ("") is not a member;
// every reader resolves it, and any other non-member, to
// ProvenanceUntrustedSource.
type Provenance string

// The two Provenance members are the whole dimension.
const (
	// ProvenanceTrusted marks content whose origin the user established:
	// their own instruction tiers, their own notes, a repository they own.
	// Instructions in trusted content may be acted on, subject to whatever
	// policy the consumer applies on top.
	ProvenanceTrusted Provenance = "trusted"
	// ProvenanceUntrustedSource marks content from somewhere the user did
	// not vouch for: a fetched page, a third-party dependency's
	// documentation, a pasted transcript, a shared corpus from another
	// scope. Text carrying this tag is data, never an instruction.
	ProvenanceUntrustedSource Provenance = "untrusted-source"
)

// Valid reports whether p is one of the two members. Anything else,
// including the zero value, is not a provenance.
func (p Provenance) Valid() bool {
	return p == ProvenanceTrusted || p == ProvenanceUntrustedSource
}

// String returns the stored spelling of p, or "invalid" for a non-member.
// It never invents a spelling for an unknown value, because a
// plausible-looking spelling is how an unknown value ends up
// round-tripping as a real one.
func (p Provenance) String() string {
	if !p.Valid() {
		return "invalid"
	}
	return string(p)
}

// ParseProvenance is the closed parser. "" is (ProvenanceUntrustedSource,
// nil): an unset provenance is untrusted by definition. Any other
// non-member - a different case, whitespace, a near spelling - is
// (ProvenanceUntrustedSource, KindInvalidInput naming the value).
func ParseProvenance(s string) (Provenance, error) {
	if s == "" {
		return ProvenanceUntrustedSource, nil
	}
	if p := Provenance(s); p.Valid() {
		return p, nil
	}
	return ProvenanceUntrustedSource, cascade.Newf(cascade.KindInvalidInput,
		"provenance: %q is not a provenance (valid: trusted, untrusted-source)", s)
}

// JoinProvenance returns ProvenanceTrusted only when every member is
// exactly ProvenanceTrusted. Any untrusted or invalid member (the empty
// string included), and the empty input itself, joins to
// ProvenanceUntrustedSource: combined content is never more trusted than
// its least trusted part, and nothing at all is not a vouched origin.
func JoinProvenance(ps ...Provenance) Provenance {
	if len(ps) == 0 {
		return ProvenanceUntrustedSource
	}
	for _, p := range ps {
		if p != ProvenanceTrusted {
			return ProvenanceUntrustedSource
		}
	}
	return ProvenanceTrusted
}
