package initconfig

// Purpose: the sorted read accessor over the schema's key registry
//   (P1-DOC-04), so docs gen lists keys from the one registry knownKeys
//   defines rather than from a second list.
// Constraints: every call returns a fresh slice, so a caller that mutates
//   the result cannot change what a later call returns.

import "slices"

// KnownKeys returns every key the schema accepts, dotted and sorted. The
// result is a new slice on each call: mutating it does not affect the
// registry or any later result.
func KnownKeys() []string {
	return slices.Sorted(slices.Values(knownKeys()))
}
