package build

import "testing"

// Purpose (this file): R-14.239's tier rule — a composition root is a role,
//   not a directory, and its siblings are not composition roots.
// SPORT: internal/build tests (ADD) — R-14.239.

// TestAPluginMainIsACompositionRootAndItsSiblingsAreNot is the test
// R-14.239 names. The second half is the load-bearing one: if a plugin's
// DECISION packages also took the CLI floor, the ruling would be a blanket
// discount on everything under plugins/ rather than a rule about the one
// package that holds the process's sockets.
func TestAPluginMainIsACompositionRootAndItsSiblingsAreNot(t *testing.T) {
	for _, tc := range []struct {
		importPath string
		isMain     bool
		wantTier   CoverageTier
		wantFloor  float64
	}{
		{"plugins/github", true, TierCLI, 70},
		{"plugins/github/auth", false, TierPlugins, 80},
		{"plugins/github/tools", false, TierPlugins, 80},
		// A library package under plugins/ is never a composition root
		// even if something mislabels it: the package clause decides.
		{"plugins/github/auth", true, TierCLI, 70},
		{"cmd/cascade", true, TierCLI, 70},
		{"providers/sqlite", false, TierPlugins, 80},
		// internal/** and the security allowlist are untouched by the rule.
		{"internal/nodes", false, TierCore, 85},
		{"internal/secrets", false, TierSecurity, 90},
	} {
		tier, floor, ok := PackageTierFor(tc.importPath, tc.isMain)
		if !ok {
			t.Errorf("%s (main=%v) resolved to no tier at all", tc.importPath, tc.isMain)
			continue
		}
		if tier != tc.wantTier || floor != tc.wantFloor {
			t.Errorf("%s (main=%v) = %s/%.0f, want %s/%.0f",
				tc.importPath, tc.isMain, tier, floor, tc.wantTier, tc.wantFloor)
		}
	}
}

// TestPackageTierDefaultsToTheLibraryFloor proves the pre-ruling entry
// point is unchanged: PackageTier alone never grants the CLI floor to a
// plugin path, so a caller that cannot read package clauses gets the
// conservative answer rather than the discount.
func TestPackageTierDefaultsToTheLibraryFloor(t *testing.T) {
	tier, floor, ok := PackageTier("plugins/github")
	if !ok || tier != TierPlugins || floor != 80 {
		t.Fatalf("PackageTier(plugins/github) = %s/%.0f (ok=%v), want plugins/80", tier, floor, ok)
	}
}

// TestTheRealTreesCompositionRootsAreFound proves the discovery half works
// against the checked-out tree, not just against literals: the rule is
// worthless if the gate cannot tell which packages are actually main.
func TestTheRealTreesCompositionRootsAreFound(t *testing.T) {
	roots, err := MainPackages(coverageModuleRoot(t))
	if err != nil {
		t.Fatalf("MainPackages: %v", err)
	}
	for _, want := range []string{"cmd/cascade", "plugins/github"} {
		if !roots[want] {
			t.Errorf("%s was not recognized as a composition root", want)
		}
	}
	for _, notRoot := range []string{"plugins/github/auth", "plugins/github/tools", "internal/nodes"} {
		if roots[notRoot] {
			t.Errorf("%s was wrongly recognized as a composition root", notRoot)
		}
	}
}
