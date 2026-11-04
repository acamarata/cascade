package runtime

// Purpose: the sorted read accessor over the one config key registry,
//   knownConfigKeys (P1-DOC-04), so docs gen lists `config set` keys from
//   the same list the verbs validate against.
// Constraints: returns a fresh sorted copy; knownConfigKeys itself is never
//   sorted in place or returned by reference.

import "slices"

// KnownConfigKeys returns every dotted-path key `config set` accepts, sorted.
// The result is a new slice on each call: mutating it does not change
// knownConfigKeys or any later result.
func KnownConfigKeys() []string {
	return slices.Sorted(slices.Values(knownConfigKeys))
}
