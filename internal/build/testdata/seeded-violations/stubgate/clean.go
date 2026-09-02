// Package violation (clean.go) is a false-positive probe: everything below
// LOOKS adjacent to an Article-1 marker but is not one, and the stub gate
// must produce ZERO findings against it.
package violation

// Package violation is a fixture package used only to exercise the stub
// gate itself — the bare word describing an incomplete work item, alone in
// prose, is not what the gate denies; only a comment that STARTS with that
// word is a directive.

// TodoListSize names a size, not an unfinished-work directive — the
// directive check only matches a comment line that begins with the
// three-or-four-letter marker word itself.
const TodoListSize = 3

// MockingbirdSong is a const identifier that happens to start with
// "Mocking", not the denied type-name prefix rule — Art.1.2 denies TYPE
// declarations beginning with the three denied prefixes, not every
// identifier that merely contains those letters, and this is a const, not
// a type declaration, so it is doubly out of scope.
const MockingbirdSong = "also not a marker"

// realImplementation is a real function with real logic; nothing here
// resembles any Article-1 marker.
func realImplementation(x int) int {
	return x * 2
}
