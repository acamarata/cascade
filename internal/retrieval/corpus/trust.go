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
// untrusted by every fail-closed reader. Combination is a fail-closed join
// so a record can never be more trusted than the corpus it came from.
//
// SPORT: internal.retrieval.corpus.TrustLevel/ADDED.

package corpus

import "github.com/acamarata/cascade/pkg/provider"

// TrustLevel is the provenance classification of a corpus source and of
// every record carved from it. It is an alias of provider.Provenance, the
// one canonical untrusted-provenance type (contract:untrusted-provenance),
// so its stored json:"trust" spelling is the provider string unchanged.
//
// The two values are the whole dimension. There is no "unknown" member:
// an unset or unrecognized level is not a third classification, it is a
// value that failed to classify, and every reader in this package resolves
// such a value to TrustUntrustedSource rather than to TrustTrusted.
type TrustLevel = provider.Provenance

// The two trust levels, aliases of the provider members.
const (
	// TrustTrusted marks content whose origin the user established.
	TrustTrusted = provider.ProvenanceTrusted
	// TrustUntrustedSource marks content from a source the user did not
	// vouch for; text carrying it is data, never an instruction.
	TrustUntrustedSource = provider.ProvenanceUntrustedSource
)

// resolveTrust returns the effective trust of a record given its own tag
// and its corpus's tag: the LESS trusted of the two, with an unset or
// unrecognized value on either side collapsing to TrustUntrustedSource.
// It is provider.JoinProvenance over the pair, so this package keeps no
// ranking table of its own.
//
// A record cannot out-rank its corpus. A corpus classified
// untrusted-source cannot contain a record that surfaces as trusted, no
// matter what the record's own row says, because the record's content came
// from that source. This is the propagation the untrusted-tag test
// asserts.
func resolveTrust(record, corpusLevel TrustLevel) TrustLevel {
	return provider.JoinProvenance(record, corpusLevel)
}
