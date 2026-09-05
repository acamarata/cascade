package context

import (
	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose: the generator's input validation. Decides which MergedContext
//   values a harness generator may render and which it must refuse, using
//   only the frozen 14-kind taxonomy in pkg/cascade.
// Inputs: a MergedContext, as produced by MergeTiers or built by a caller.
// Outputs: nil, or a typed cascade.Error of KindInvalidInput.
// Constraints: fail closed. A MergedContext this package cannot vouch for
//   is refused whole; rendering the parts of it that happen to look fine
//   would ship an instruction file missing the parts that did not, with
//   nothing on the page to say so.
// SPORT: context-engine/cc-instruction-gen (ADD, per T-3 sport_updates).

// validateMergedContext rejects a MergedContext that cannot have come from
// MergeTiers.
//
// # Why an empty context is not an error
//
// A working directory with no instruction files anywhere above it is a
// legitimate, common state, and it merges to a MergedContext with no
// sections. That renders to no files, which is the correct answer, so it
// must not be confused with the malformed cases below.
//
// # What a nil MergedContext is, in a language without one
//
// The contract for this generator names a "nil MergedContext" as an error
// case. MergedContext is a struct, so there is no nil to be handed: the
// reachable equivalent is a value carrying sections while its Provenance
// map is nil, which is what a caller who built the struct by hand instead
// of calling MergeTiers produces. MergeTiers always returns a non-nil
// Provenance, so this can only be a hand-built value, and a hand-built
// value whose provenance is missing cannot be checked for the very
// override it exists to record.
func validateMergedContext(mc MergedContext) error {
	if len(mc.Sections) == 0 {
		return nil
	}
	if mc.Provenance == nil {
		return cascade.New(cascade.KindInvalidInput,
			"context: generate: the merged context carries sections but no provenance map; it did not come from MergeTiers")
	}
	prevOrdinal := -1
	for i, s := range mc.Sections {
		if !s.Role.Valid() {
			return cascade.Newf(cascade.KindInvalidInput,
				"context: generate: section %d has an invalid tier role (%d)", i, uint8(s.Role))
		}
		if s.Ordinal < 0 {
			return cascade.Newf(cascade.KindInvalidInput,
				"context: generate: section %d (tier %s) has a negative ordinal (%d)", i, s.Role, s.Ordinal)
		}
		if s.Ordinal < prevOrdinal {
			return cascade.Newf(cascade.KindInvalidInput,
				"context: generate: section %d (tier %s) has ordinal %d after %d; sections must arrive in merge order",
				i, s.Role, s.Ordinal, prevOrdinal)
		}
		prevOrdinal = s.Ordinal
		if s.Heading == "" {
			continue
		}
		winner, keyed := mc.Provenance[s.Heading]
		if !keyed {
			return cascade.Newf(cascade.KindInvalidInput,
				"context: generate: section %d heading %q is not recorded in the provenance map", i, s.Heading)
		}
		if winner != s.Role {
			return cascade.Newf(cascade.KindInvalidInput,
				"context: generate: section %d heading %q is carried by tier %s but provenance records tier %s as its winner",
				i, s.Heading, s.Role, winner)
		}
	}
	return nil
}
