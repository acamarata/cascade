// Purpose: the TRUST dimension of the corpus model. A corpus source and
// every record carved from it carry an explicit trusted | untrusted-source
// classification, and that tag rides intact through the scope-filtered
// query API so context assembly and the auto-advance ceiling can refuse to
// act on instructions that came from an untrusted source. This package
// defines the dimension; it never enforces it, because the enforcement
// point is the consumer that decides whether to obey text, not the store
// that hands the text over.
//
// Inputs: a trust string as written in a corpus definition or read back
// from storage.
//
// Outputs: a TrustLevel, or the refusal that an unrecognized value is not
// a trust level.
//
// Constraints: exactly the two values the plan names, no third state that
// silently means "probably fine". The zero value is deliberately invalid,
// so a TrustLevel that was never set fails Valid and is treated as
// untrusted by every fail-closed reader. Ordering is by trustworthiness so
// a record can never be more trusted than the corpus it came from.
//
// SPORT: internal.retrieval.corpus.TrustLevel/ADDED.

package corpus

// TrustLevel is the provenance classification of a corpus source and of
// every record carved from it.
//
// The two values are the whole dimension. There is no "unknown" member:
// an unset or unrecognized level is not a third classification, it is a
// value that failed to classify, and every reader in this package resolves
// such a value to TrustUntrustedSource rather than to TrustTrusted.
type TrustLevel string

const (
	// TrustTrusted marks content whose origin the user established: their
	// own instruction tiers, their own notes, a repository they own.
	// Instructions found in trusted content may be acted on, subject to
	// whatever policy the consumer applies on top.
	TrustTrusted TrustLevel = "trusted"

	// TrustUntrustedSource marks content that arrived from somewhere the
	// user did not vouch for: a fetched page, a third-party dependency's
	// documentation, a pasted transcript, a shared corpus from another
	// scope. Text carrying this tag is data. It is never an instruction,
	// and the auto-advance ceiling refuses to advance on it.
	TrustUntrustedSource TrustLevel = "untrusted-source"
)

// Valid reports whether t is one of the two defined levels. Anything else,
// including the zero value, is not a level.
func (t TrustLevel) Valid() bool {
	return t == TrustTrusted || t == TrustUntrustedSource
}

// String returns the stored spelling of t, or "invalid" for a value that
// is not a defined level. It never invents a spelling for an unknown
// value, because a plausible-looking spelling is how an unknown value ends
// up round-tripping as a real one.
func (t TrustLevel) String() string {
	if !t.Valid() {
		return "invalid"
	}
	return string(t)
}

// trustRank orders the levels from least to most trusted so restrictive
// combination (leastTrust) is a plain comparison. An invalid value ranks
// below the lowest defined level, which is what makes an unreadable trust
// tag resolve to untrusted rather than to trusted.
func trustRank(t TrustLevel) int {
	switch t {
	case TrustUntrustedSource:
		return 1
	case TrustTrusted:
		return 2
	default:
		return 0
	}
}

// resolveTrust returns the effective trust of a record given its own tag
// and its corpus's tag: the LESS trusted of the two, with an unset or
// unrecognized value on either side collapsing to TrustUntrustedSource.
//
// A record cannot out-rank its corpus. A corpus classified
// untrusted-source cannot contain a record that surfaces as trusted, no
// matter what the record's own row says, because the record's content came
// from that source. This is the propagation the untrusted-tag test
// asserts.
func resolveTrust(record, corpusLevel TrustLevel) TrustLevel {
	if !record.Valid() || !corpusLevel.Valid() {
		return TrustUntrustedSource
	}
	if trustRank(record) <= trustRank(corpusLevel) {
		return record
	}
	return corpusLevel
}
