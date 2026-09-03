package build

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// coverageModuleRoot locates the repo root by walking up from this file.
func coverageModuleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("coverage gate: runtime.Caller(0) failed")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("coverage gate: no go.mod found walking up from %s", file)
		}
		dir = parent
	}
}

const coverageModulePath = "github.com/acamarata/cascade"

// coverageLoadBaseline reads the real, committed baseline
// (testdata/coverage-baseline.json).
func coverageLoadBaseline(t *testing.T, root string) map[string]BaselineEntry {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "internal", "build", "testdata", "coverage-baseline.json"))
	if err != nil {
		t.Fatalf("coverage gate: reading baseline: %v", err)
	}
	baseline, err := ParseBaseline(data)
	if err != nil {
		t.Fatalf("coverage gate: parsing baseline: %v", err)
	}
	if len(baseline) == 0 {
		t.Fatal("coverage gate: baseline parsed to zero entries — regression, not a legitimately empty baseline")
	}
	return baseline
}

// coverageStripAll strips the module prefix from every key in profile,
// per StripModulePrefix.
func coverageStripAll(profile map[string]*CoverageStats) map[string]*CoverageStats {
	out := make(map[string]*CoverageStats, len(profile))
	for k, v := range profile {
		out[StripModulePrefix(k, coverageModulePath)] = v
	}
	return out
}

// CoverageProfileEnvVar names the environment variable pointing at an
// already-generated coverage profile (the ci.yml `coverage` job's
// coverage.out artifact). This gate does NOT run the test suite itself and
// invoke `go test ./...` as a CHILD of a `go test` process already
// running: probed directly, that nested invocation contends on the shared
// build-cache lock long enough to blow past a 5-minute timeout — a real,
// reproducible hang, not a hypothetical one. The live check is therefore
// env-var-gated exactly like TestConventionalCommitGate_Live and
// TestIdentifierSweepGate_Live in commits.go/sweep.go: skipped locally by
// default, run for real only where a profile already exists (a dedicated
// coverage-gate CI job downloading the coverage lane's artifact, or a
// developer pointing it at their own `go test -coverprofile` output).
const CoverageProfileEnvVar = "CASCADE_COVERAGE_PROFILE"

// TestCoverageGate_Live reads the profile CoverageProfileEnvVar names and
// asserts zero floor/ratchet violations against the committed baseline —
// this is the gate exactly as ci.yml's coverage-gate job runs it.
func TestCoverageGate_Live(t *testing.T) {
	profilePath := os.Getenv(CoverageProfileEnvVar)
	if profilePath == "" {
		t.Skipf("coverage gate: set %s to a coverage.out path to run this check (see .github/workflows/ci.yml coverage-gate job)", CoverageProfileEnvVar)
	}
	root := coverageModuleRoot(t)

	data, err := os.ReadFile(profilePath)
	if err != nil {
		t.Fatalf("coverage gate: reading profile %s: %v", profilePath, err)
	}
	profile, err := ParseCoverageProfile(data)
	if err != nil {
		t.Fatalf("coverage gate: parsing generated profile: %v", err)
	}
	stripped := coverageStripAll(profile)
	baseline := coverageLoadBaseline(t, root)

	v := CheckCoverage(stripped, baseline)
	// R-14.155: floor/ratchet checking alone cannot see a package whose
	// profile entry is missing entirely (a test binary that failed to
	// compile, or panicked before the profile was written) — completeness
	// is a second, independent check against every package the checked-out
	// tree actually contains, not just the ones the profile mentions.
	expected, err := DiscoverPackages(root)
	if err != nil {
		t.Fatalf("coverage gate: discovering expected packages: %v", err)
	}
	v = append(v, CheckCompleteness(stripped, expected)...)
	if len(v) != 0 {
		t.Fatalf("coverage gate: %d violation(s) against the real tree: %+v", len(v), v)
	}
}

func coverageFixtureDir(t *testing.T) string {
	t.Helper()
	return filepath.Join(coverageModuleRoot(t), "internal", "build", "testdata", "seeded-violations", "coverage")
}

// TestCoverageGate_SeededFloorBreachRed proves the floor half: a synthetic
// profile puts internal/policy (security tier, floor 90%) at 50% coverage.
func TestCoverageGate_SeededFloorBreachRed(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(coverageFixtureDir(t), "floor-breach.profile"))
	if err != nil {
		t.Fatalf("coverage gate: reading fixture: %v", err)
	}
	profile, err := ParseCoverageProfile(data)
	if err != nil {
		t.Fatalf("coverage gate: parsing fixture profile: %v", err)
	}
	stripped := coverageStripAll(profile)
	root := coverageModuleRoot(t)
	baseline := coverageLoadBaseline(t, root)

	v := CheckCoverage(stripped, baseline)
	var found bool
	for _, viol := range v {
		if viol.Package == "internal/policy" && viol.Reason == "floor" {
			found = true
			if viol.Measured >= viol.Floor {
				t.Errorf("coverage gate: violation reports measured %.1f >= floor %.1f, contradiction", viol.Measured, viol.Floor)
			}
		}
	}
	if !found {
		t.Fatalf("coverage gate: expected an internal/policy floor violation, got %+v", v)
	}
}

// TestCoverageGate_SeededRatchetDropRed proves the ratchet half: a
// synthetic profile puts internal/build at 86% — above its 85% Art.4 floor
// (no floor violation) but below its committed baseline of 90% (a ratchet
// violation).
func TestCoverageGate_SeededRatchetDropRed(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(coverageFixtureDir(t), "ratchet-drop.profile"))
	if err != nil {
		t.Fatalf("coverage gate: reading fixture: %v", err)
	}
	profile, err := ParseCoverageProfile(data)
	if err != nil {
		t.Fatalf("coverage gate: parsing fixture profile: %v", err)
	}
	stripped := coverageStripAll(profile)
	root := coverageModuleRoot(t)
	baseline := coverageLoadBaseline(t, root)

	v := CheckCoverage(stripped, baseline)
	var found bool
	for _, viol := range v {
		if viol.Package == "internal/build" {
			found = true
			if viol.Reason != "ratchet" {
				t.Errorf("coverage gate: expected reason \"ratchet\", got %q", viol.Reason)
			}
			if viol.Measured < viol.Floor {
				t.Errorf("coverage gate: this fixture is meant to prove a ratchet drop ABOVE the floor; measured %.1f < floor %.1f", viol.Measured, viol.Floor)
			}
		}
	}
	if !found {
		t.Fatalf("coverage gate: expected an internal/build ratchet violation, got %+v", v)
	}
}

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

// TestCoverageGate_ZeroStatementPackagesAreSkipped proves a package with
// no shippable statements yet (a doc.go-only placeholder) never produces a
// violation merely for existing.
func TestCoverageGate_ZeroStatementPackagesAreSkipped(t *testing.T) {
	profile := map[string]*CoverageStats{
		"internal/conductor": {CoveredStmts: 0, TotalStmts: 0},
	}
	baseline := map[string]BaselineEntry{}
	v := CheckCoverage(profile, baseline)
	if len(v) != 0 {
		t.Fatalf("coverage gate: expected zero violations for a zero-statement package, got %+v", v)
	}
}

// TestCoverageGate_PkgIsExcluded proves pkg/** is never floor-checked
// (Art.10.5/10.6 govern it instead; see coveragegate.go's package doc).
func TestCoverageGate_PkgIsExcluded(t *testing.T) {
	profile := map[string]*CoverageStats{
		"pkg/provider": {CoveredStmts: 0, TotalStmts: 40},
	}
	baseline := map[string]BaselineEntry{}
	v := CheckCoverage(profile, baseline)
	if len(v) != 0 {
		t.Fatalf("coverage gate: expected pkg/provider (0%% coverage) to be excluded from Art.4 floors, got %+v", v)
	}
}
