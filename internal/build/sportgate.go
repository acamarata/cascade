// Package build (this file) is the SPORT malformed-line gate: the one
// thing internal/inventory/sport's registry treats as a HARD failure
// rather than a reported number (see that package's registry.go doc
// comment for the full reasoning). A "// SPORT:" marker line that this
// tree's grammar cannot extract even a name from is an entity that would
// otherwise silently vanish from the registry — exactly the failure mode
// the owner's original ask ("a good SPORT model ... this changes all the
// time") was raised against when SPORTLines turned out to be a raw line
// count and not a queryable registry.
//
// What this gate does NOT do, stated here per this package's own
// convention (every gate owes its blind spot in its header):
//   - It does not fail on a CONTRADICTORY status across an entity's
//     sites (ADD at one site, CHANGED at another) — internal/inventory/
//     sport.Registry.MultiStatusCount reports that as a number because an
//     entity evolving from ADD to CHANGED over time is normal history,
//     not a defect, and this gate has no ordering information (git blame
//     timestamps are not tracked here) to distinguish real drift from
//     that.
//   - It does not fail on an entity declared with StatusUnspecified (a
//     marker with a real name but no recognized status verb) — that is
//     also reported (Registry.UnspecifiedCount), not failed, because the
//     live tree already carries legitimate description-only markers
//     (doc.go's "cmd/cascade — cobra-root, ..." example) that predate
//     this gate and are not themselves wrong.
//   - It only scans the files it is given; a file outside the tracked
//     *.go set (this repo's markers never appear elsewhere) is invisible
//     to it, same limitation as internal/inventory's own SPORTLines walk.
//
// SPORT: internal.build.CheckSportMalformed/ADDED.
package build

import (
	"path/filepath"
	"strings"

	"github.com/acamarata/cascade/internal/inventory/sport"
)

// CheckSportMalformed scans trackedFiles under root and returns every
// malformed "// SPORT:" line found — a line ParseMarkerLine could not
// extract even a name from. Zero violations is the gate passing.
func CheckSportMalformed(root string, trackedFiles []string) ([]sport.ParseError, error) {
	_, errs, err := sport.ScanFiles(root, trackedFiles)
	if err != nil {
		return nil, err
	}
	return errs, nil
}

// SportGateSkipsPath reports whether rel is under a testdata/ directory —
// this gate's seeded-violation fixtures deliberately contain a malformed
// marker, and the real-tree check must not scan its own fixtures, same
// rationale as CountsDriftSkipsPath and sweep.go's SweepSkipsPath.
func SportGateSkipsPath(rel string) bool {
	for _, seg := range strings.Split(filepath.ToSlash(rel), "/") {
		if seg == "testdata" {
			return true
		}
	}
	return false
}
