// Package healthy is affected_go_fixture_broken's only real Go package.
// TestAffectedTargets_UnrelatedBrokenPackageForcesFull changes this file
// while pkg/broken (an orphan .c file, no .go file at all) sits
// elsewhere in the SAME module, untouched -- breaking the whole-tree
// `go list ./...` call this fixture exists to break, proving
// affectedGoTargets fails CLOSED to TargetAll rather than returning a
// raw error (D1 REWORK round 2 fix, confirm review case 3).
package healthy

// F is a trivial exported func so this package is a real, independently
// buildable Go package on its own -- only the whole-tree `./...` call
// (which also visits pkg/broken) fails.
func F() int { return 1 }
