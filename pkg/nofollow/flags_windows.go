//go:build windows

// Purpose: the windows values of the open flags: none exist.
//
// Inputs: none.
// Outputs: NoFollow and NonBlock, both 0.
// Constraints: see doc.go; a 0 flag guards nothing.
//
// SPORT: pkg/nofollow (ADD) — P1-AGT-01.

package nofollow

// NoFollow and NonBlock are 0 on windows; see the package doc.
const (
	NoFollow int = 0
	NonBlock int = 0
)
