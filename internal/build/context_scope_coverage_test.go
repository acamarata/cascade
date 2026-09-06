package build

// Purpose: registers internal/context/scope in the Art.4 coverage-floor
//   ratchet baseline and asserts the registration itself (tier, floor,
//   and a real measured baseline this ticket's own coverage run
//   produced -- see the ticket journal for the exact `go test -cover`
//   output this number comes from).
// SPORT: internal/build (CHANGE, per T-4 sport_updates).

import "testing"

// TestContextScopeCoverageFloorRegistration asserts internal/context/scope
// carries a committed baseline entry at the core tier's 85% floor, with a
// baseline value that does not exceed what this package's own test suite
// actually measured (87.6% statements, `go test -cover` at the time this
// ticket landed) -- never a number no run has produced.
func TestContextScopeCoverageFloorRegistration(t *testing.T) {
	root := coverageModuleRoot(t)
	baseline := coverageLoadBaseline(t, root)

	const pkg = "internal/context/scope"
	entry, ok := baseline[pkg]
	if !ok {
		t.Fatalf("coverage baseline: %s is not registered in testdata/coverage-baseline.json", pkg)
	}

	tier, floor, ok := PackageTier(pkg)
	if !ok {
		t.Fatalf("PackageTier(%q) ok=false, want a real tier classification", pkg)
	}
	if tier != TierCore {
		t.Errorf("PackageTier(%q) = %q, want %q", pkg, tier, TierCore)
	}
	if floor != coverageTierFloors[TierCore] {
		t.Errorf("PackageTier(%q) floor = %v, want %v", pkg, floor, coverageTierFloors[TierCore])
	}
	if entry.Tier != string(TierCore) {
		t.Errorf("baseline entry tier = %q, want %q", entry.Tier, TierCore)
	}
	if entry.Floor != coverageTierFloors[TierCore] {
		t.Errorf("baseline entry floor = %v, want %v", entry.Floor, coverageTierFloors[TierCore])
	}
	if entry.Baseline < entry.Floor {
		t.Errorf("baseline entry baseline %v is below its own floor %v", entry.Baseline, entry.Floor)
	}
	if entry.Baseline > 87.6+coverageEpsilon {
		t.Errorf("baseline entry baseline %v exceeds the measured coverage (87.6%%) this ticket recorded -- never register a number no run produced", entry.Baseline)
	}
}
