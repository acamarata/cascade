package evidence

// Purpose: DataClass is this package's closed data-sensitivity lattice
// (R-21.94) and Join, the single primitive AQ/S-83.T2 and AQ/S-83.T3 call
// for their per-partition and per-item derivations.
//
// CONTRACT/TREE CONTRADICTION (quoted in full in this ticket's journal):
// this ticket's contract text describes a four-value lattice
// "local-only > restricted > internal > public" with no counterpart
// anywhere else in the tree. internal/jobs/model.go's DataClass (R-21.94,
// already shipped by P1-E29-W6-S59-T1) and internal/policy/types.go's
// DataClass (R-21.27) both establish the ACTUAL four-value vocabulary in
// force across the repo: public < internal < confidential < secret --
// and internal/jobs/store_exec.go's Artifact.DataClass (the exact column
// this ticket's item 10/11 targets) is ALREADY that type, already
// immutable. jobs/model.go's own doc comment names this ticket as the
// future joiner: "matching internal/policy.DataClass's names exactly so a
// future caller that DOES join the two (AQ/S-83) sees identical wire
// values". Inventing a second, incompatible vocabulary here would break
// interoperability at the exact boundary this ticket is required to
// touch, so this file mirrors jobs.DataClass's four values BY VALUE
// (package-local type, no cross-package import, matching jobs/model.go's
// own precedent for the identical situation) rather than the contract's
// invented lattice.
//
// Inputs: a stored TEXT value (DecodeDataClass) or a set of DataClass
// values to combine (Join).
// Outputs: a DataClass, or a typed invalid-input error on an unknown
// value.
// Constraints: no permissive zero value -- DecodeDataClass refuses rather
// than silently substituting a default, matching jobs.DecodeDataClass's
// own fail-closed precedent (stronger than the contract's "unset =>
// restricted" partial-permissive rule, and the established behavior this
// ticket follows per AGENT-BRIEF's "follow the tree" instruction).
//
// SPORT: evidence/data-class (ADD), R-21.94.

import "github.com/acamarata/cascade/pkg/cascade"

// DataClass is the sensitivity tier of the material a Claim or Evidence
// row was extracted from, or an Artifact record carries. Immutable once
// stored (R-21.94) -- enforced in claim_store.go/evidence_store.go, not
// here.
type DataClass string

// The closed DataClass vocabulary, ordered least to most sensitive,
// matching internal/jobs.DataClass and internal/policy.DataClass exactly.
const (
	DataClassPublic       DataClass = "public"
	DataClassInternal     DataClass = "internal"
	DataClassConfidential DataClass = "confidential"
	DataClassSecret       DataClass = "secret"
)

// dataClassRank orders the four values for Join's MAX computation. An
// invalid value is never looked up here -- Join resolves it to
// DataClassSecret (the most restrictive member) before ranking, so an
// undeterminable input can never rank below a known one.
var dataClassRank = map[DataClass]int{
	DataClassPublic:       0,
	DataClassInternal:     1,
	DataClassConfidential: 2,
	DataClassSecret:       3,
}

// Valid reports whether d is one of the four closed values.
func (d DataClass) Valid() bool {
	_, ok := dataClassRank[d]
	return ok
}

// DecodeDataClass parses a stored TEXT value into a DataClass. An unknown
// or empty value returns a typed invalid-input error -- there is no
// permissive zero value.
func DecodeDataClass(raw string) (DataClass, error) {
	d := DataClass(raw)
	if !d.Valid() {
		return "", cascade.Newf(cascade.KindInvalidInput,
			"evidence: unknown data_class %q: must be one of public, internal, confidential, secret", raw)
	}
	return d, nil
}

// Join returns the lattice MAX (the most restrictive member) over
// classes. An empty call, or any member that is unset/unknown/invalid,
// resolves to DataClassSecret -- the fail-closed direction: an
// undeterminable class can only raise the joined result, never lower it.
func Join(classes ...DataClass) DataClass {
	result := DataClassPublic
	if len(classes) == 0 {
		return DataClassSecret
	}
	for _, c := range classes {
		if !c.Valid() {
			c = DataClassSecret
		}
		if dataClassRank[c] > dataClassRank[result] {
			result = c
		}
	}
	return result
}
