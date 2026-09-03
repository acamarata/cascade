package build

// Purpose: completeness-check tests (R-14.155) — split out of
//   coveragegate_test.go under R-14.117/R-14.133 (Art.10.3's 300-line file
//   cap; that file grew past it once this ticket added
//   DiscoverPackages/CheckCompleteness coverage). Same package, no
//   behaviour change. coverageFixtureDir/coverageStripAll/
//   coverageModuleRoot/coverageLoadBaseline stay defined in
//   coveragegate_test.go and remain usable here (same package).
// SPORT: internal/build (ADD, per T-2 sport_updates).

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCoverageGate_SeededMissingPackageRed proves the completeness half
// (R-14.155): a synthetic profile that mentions only internal/buildinfo
// (this repo's smallest core package) — exactly what a truncated run
// produces when one package's test binary fails to compile or panics
// before `go test -coverprofile` writes its line — must make
// CheckCompleteness report every OTHER floor-bearing package in the real
// tree as "missing", including internal/daemon: the package this exact
// failure mode made disappear from the live gate before this fix (see
// this file's package doc and R-14.155's ruling).
func TestCoverageGate_SeededMissingPackageRed(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(coverageFixtureDir(t), "missing-package.profile"))
	if err != nil {
		t.Fatalf("coverage gate: reading fixture: %v", err)
	}
	profile, err := ParseCoverageProfile(data)
	if err != nil {
		t.Fatalf("coverage gate: parsing fixture profile: %v", err)
	}
	stripped := coverageStripAll(profile)
	root := coverageModuleRoot(t)
	expected, err := DiscoverPackages(root)
	if err != nil {
		t.Fatalf("coverage gate: discovering expected packages: %v", err)
	}

	v := CheckCompleteness(stripped, expected)
	var found bool
	for _, viol := range v {
		if viol.Package == "internal/daemon" {
			found = true
			if viol.Reason != "missing" {
				t.Errorf("coverage gate: expected reason \"missing\", got %q", viol.Reason)
			}
			if viol.Measured != 0 {
				t.Errorf("coverage gate: a missing package has nothing measured, got %.1f", viol.Measured)
			}
		}
	}
	if !found {
		t.Fatalf("coverage gate: expected an internal/daemon \"missing\" violation, got %+v", v)
	}
	// internal/buildinfo IS in the fixture profile — it must never be
	// reported missing merely for being small or for being the one
	// package this fixture kept.
	for _, viol := range v {
		if viol.Package == "internal/buildinfo" {
			t.Errorf("coverage gate: internal/buildinfo is present in the fixture profile, must not be reported missing: %+v", viol)
		}
	}
}

// TestDiscoverPackages_DefaultBuildableIsExpected is coordinator probe #1:
// a package that IS buildable under default tags (internal/daemon — no
// package-wide build constraint; lifecycle_unix.go's own `//go:build
// !windows` is satisfied by default on every non-Windows CI/dev host)
// must appear in DiscoverPackages' output. Combined with
// TestCoverageGate_SeededMissingPackageRed (which proves a profile
// missing internal/daemon's entry is reported "missing"), this is the
// exact failure mode that let internal/daemon disappear from the gate
// earlier today: default-buildable, but its test binary failed to
// compile, so the profile carried no entry for it. If DiscoverPackages
// ever stopped expecting internal/daemon, that scenario would silently
// stop being a violation again.
func TestDiscoverPackages_DefaultBuildableIsExpected(t *testing.T) {
	root := coverageModuleRoot(t)
	expected, err := DiscoverPackages(root)
	if err != nil {
		t.Fatalf("coverage gate: discovering expected packages: %v", err)
	}
	if !slicesContain(expected, "internal/daemon") {
		t.Fatalf("coverage gate: expected internal/daemon (default-buildable) in %v", expected)
	}
}

// TestDiscoverPackages_TagGatedPackageIsNotExpected is coordinator probe
// #2: providers/postgres (every file behind `//go:build postgres`) must
// NOT appear in DiscoverPackages' output — it can never satisfy default
// tags, so it can never appear in a default-tag coverage profile, and
// "absent from the profile" is its permanent, correct state, not a
// violation. This is not a name-based exemption: discoverDirHasShippableGoFile
// asks go/build.ImportDir (the standard library's own build-constraint
// evaluator, the same logic `go list`/`go build` use) whether ANY file in
// the directory survives DEFAULT tags on this host; providers/postgres
// fails that for every file it has (*build.NoGoError), which is exactly
// why it is excluded — the same mechanism would exclude the next
// tag-gated package without anyone adding its name anywhere.
func TestDiscoverPackages_TagGatedPackageIsNotExpected(t *testing.T) {
	root := coverageModuleRoot(t)
	expected, err := DiscoverPackages(root)
	if err != nil {
		t.Fatalf("coverage gate: discovering expected packages: %v", err)
	}
	if slicesContain(expected, "providers/postgres") {
		t.Fatalf("coverage gate: providers/postgres (build-tag postgres, never satisfied by default) must not be expected, got it in %v", expected)
	}
}

func slicesContain(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// TestCoverageGate_CompletenessGreenOnRealTree proves the other direction:
// DiscoverPackages against the real checked-out tree, checked against a
// profile that genuinely covers every one of those packages, reports zero
// completeness violations — completeness passing is not just "the fixture
// wasn't run".
func TestCoverageGate_CompletenessGreenOnRealTree(t *testing.T) {
	root := coverageModuleRoot(t)
	expected, err := DiscoverPackages(root)
	if err != nil {
		t.Fatalf("coverage gate: discovering expected packages: %v", err)
	}
	if len(expected) == 0 {
		t.Fatal("coverage gate: DiscoverPackages found zero packages in the real tree — regression")
	}
	// A profile that already lists every expected package (measured
	// doesn't matter here — only presence) must never trip completeness.
	full := make(map[string]*CoverageStats, len(expected))
	for _, pkg := range expected {
		full[pkg] = &CoverageStats{CoveredStmts: 1, TotalStmts: 1}
	}
	if v := CheckCompleteness(full, expected); len(v) != 0 {
		t.Fatalf("coverage gate: expected zero completeness violations for a fully-present profile, got %+v", v)
	}
}
