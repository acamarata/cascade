// Purpose: baseline-completeness and generator-logic tests (AUD-040) —
//
//	CheckBaselineCompleteness, AddMissingBaselineEntries and
//	FormatBaselineJSON, split from coveragegate_test.go/
//	coveragegate_completeness_test.go under Art.10.3's 300-line file cap.
//	Same package; coverageFixtureDir/coverageStripAll/coverageModuleRoot/
//	coverageLoadBaseline (defined in coveragegate_test.go) are reused here.
//
// SPORT: internal/build (ADD, per T-2 sport_updates).
package build

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCoverageGate_SeededMissingBaselineRed proves the baseline-completeness
// half (AUD-040): a synthetic profile mentions internal/policy — fully
// covered, so no floor problem — but its baseline entry is deliberately
// removed from a copy of the real committed baseline (the real baseline
// legitimately DOES carry an internal/policy entry once this ticket's own
// generator run has filled it — deleting it here is what makes this a
// "no entry" fixture, not an assumption about the live file's current
// contents). CheckBaselineCompleteness must report it, reason
// "no-baseline": today CheckCoverage's own doc comment states a package
// with no baseline entry is "NOT a violation by itself", which means most
// of this repo's floor-bearing packages could drop coverage after their
// first measurement and never trip the ratchet again.
func TestCoverageGate_SeededMissingBaselineRed(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(coverageFixtureDir(t), "missing-baseline.profile"))
	if err != nil {
		t.Fatalf("coverage gate: reading fixture: %v", err)
	}
	profile, err := ParseCoverageProfile(data)
	if err != nil {
		t.Fatalf("coverage gate: parsing fixture profile: %v", err)
	}
	stripped := coverageStripAll(profile)
	baseline := coverageLoadBaseline(t, coverageModuleRoot(t))
	delete(baseline, "internal/policy")

	v := CheckBaselineCompleteness(stripped, baseline)
	var found bool
	for _, viol := range v {
		if viol.Package == "internal/policy" {
			found = true
			if viol.Reason != "no-baseline" {
				t.Errorf("coverage gate: expected reason \"no-baseline\", got %q", viol.Reason)
			}
		}
	}
	if !found {
		t.Fatalf("coverage gate: expected an internal/policy \"no-baseline\" violation, got %+v", v)
	}
}

// TestCoverageGate_BaselineCompletenessSkipsZeroStatements proves a
// package the profile mentions at zero total statements is never reported
// "no-baseline" — nothing to ratchet, same exemption CheckCoverage already
// grants (coveragegate.go's package doc).
func TestCoverageGate_BaselineCompletenessSkipsZeroStatements(t *testing.T) {
	profile := map[string]*CoverageStats{"internal/conductor": {CoveredStmts: 0, TotalStmts: 0}}
	if v := CheckBaselineCompleteness(profile, map[string]BaselineEntry{}); len(v) != 0 {
		t.Fatalf("coverage gate: expected zero violations for a zero-statement package, got %+v", v)
	}
}

// TestCoverageGate_BaselineCompletenessSkipsPkg proves pkg/** (excluded
// from every Art.4 floor) is never reported "no-baseline" either.
func TestCoverageGate_BaselineCompletenessSkipsPkg(t *testing.T) {
	profile := map[string]*CoverageStats{"pkg/provider": {CoveredStmts: 0, TotalStmts: 40}}
	if v := CheckBaselineCompleteness(profile, map[string]BaselineEntry{}); len(v) != 0 {
		t.Fatalf("coverage gate: expected pkg/provider excluded from baseline-completeness, got %+v", v)
	}
}

// TestCoverageBaselineAddMissingNeverChangesExisting proves the
// generator's core contract: given the real committed baseline (which has
// an "internal/build" entry with baseline 90.0) and a synthetic profile
// that measures internal/build LOWER (86%, below its committed baseline
// but above its 85% floor — a real ratchet-worthy drop) plus a second
// package made missing by deliberately deleting its entry
// (internal/policy — fully covered in the profile below), the real
// committed baseline may itself already carry an internal/policy entry
// (this ticket's own generator run fills it), so the delete is what makes
// this a "missing" fixture rather than a claim about live file contents.
// AddMissingBaselineEntries must (1) leave the internal/build entry
// byte-value-identical to what ParseBaseline loaded — never overwritten
// with the lower measured value — and (2) add exactly one new entry, for
// internal/policy only.
func TestCoverageBaselineAddMissingNeverChangesExisting(t *testing.T) {
	root := coverageModuleRoot(t)
	existing := coverageLoadBaseline(t, root)
	delete(existing, "internal/policy")
	originalBuildEntry, present := existing["internal/build"]
	if !present || originalBuildEntry.Baseline <= 86.0 {
		t.Fatalf("coverage gate: fixture assumption broken — need an internal/build baseline entry above 86.0, got %+v (present=%v)", originalBuildEntry, present)
	}

	profile := map[string]*CoverageStats{
		"internal/build":  {CoveredStmts: 86, TotalStmts: 100}, // measures 86%, below the 90.0 baseline
		"internal/policy": {CoveredStmts: 5, TotalStmts: 5},    // missing entirely, 100% measured
	}

	updated, added := AddMissingBaselineEntries(existing, profile, nil)

	if got := updated["internal/build"]; got != originalBuildEntry {
		t.Fatalf("coverage gate: existing internal/build entry changed: got %+v, want unchanged %+v", got, originalBuildEntry)
	}
	if len(added) != 1 || added[0] != "internal/policy" {
		t.Fatalf("coverage gate: expected added == [internal/policy], got %v", added)
	}
	want := BaselineEntry{Tier: "security", Floor: 90.0, Baseline: 100.0}
	if got := updated["internal/policy"]; got != want {
		t.Fatalf("coverage gate: internal/policy entry = %+v, want %+v", got, want)
	}

	// Every other pre-existing entry must also survive untouched.
	for k, v := range existing {
		if k == "internal/build" {
			continue
		}
		if got := updated[k]; got != v {
			t.Errorf("coverage gate: pre-existing entry %q changed: got %+v, want %+v", k, got, v)
		}
	}
}

// TestFormatBaselineJSON_RoundTripsByteIdentical proves FormatBaselineJSON
// is a stable fixed point: rendering a baseline, parsing that rendering
// back, and rendering it again produces byte-identical output — the
// property TestCoverageBaselineAddMissingNeverChangesExisting depends on
// to call an entry "byte-identical" (it is not enough for the Go struct
// value to compare equal; the SERIALIZED form the generator writes back
// to disk must match too). A synthetic baseline is used here; the tracked
// file has its own fixed-point test.
func TestFormatBaselineJSON_RoundTripsByteIdentical(t *testing.T) {
	entries := map[string]BaselineEntry{
		"cmd/cascade":      {Tier: "cli", Floor: 70.0, Baseline: 80.0},
		"internal/build":   {Tier: "core", Floor: 85.0, Baseline: 90.0},
		"providers/sqlite": {Tier: "plugins", Floor: 80.0, Baseline: 86.1},
	}
	first := FormatBaselineJSON(entries)
	parsed, err := ParseBaseline(first)
	if err != nil {
		t.Fatalf("coverage gate: parsing FormatBaselineJSON's own output: %v", err)
	}
	second := FormatBaselineJSON(parsed)
	if string(first) != string(second) {
		t.Fatalf("coverage gate: FormatBaselineJSON did not round-trip byte-identically\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
}

// TestBaselineFloorTruncate1dp proves the "never claims more than
// measured" rule: truncation rounds DOWN, never up, at exactly one decimal
// place.
func TestBaselineFloorTruncate1dp(t *testing.T) {
	cases := map[float64]float64{
		90.06: 90.0,
		90.09: 90.0,
		90.0:  90.0,
		0.0:   0.0,
		99.99: 99.9,
	}
	for in, want := range cases {
		if got := baselineFloorTruncate1dp(in); got != want {
			t.Errorf("coverage gate: baselineFloorTruncate1dp(%v) = %v, want %v", in, got, want)
		}
	}
}

// testCoverageRatchetDropOnNewBaselineEntry proves the ratchet catches a
// 1.0-point drop on a baseline entry OUTSIDE coverageOriginalEleven — a
// package this ticket's generator baselined, not a hand-curated one —
// otherwise "the ratchet covers every package" would be true only for
// packages someone remembered to add by hand, exactly the defect AUD-040
// exists to close. Called from TestCoverageGate_SeededRatchetDropRed in
// coveragegate_test.go (Art.10.3's 300-line cap moved it here).
func testCoverageRatchetDropOnNewBaselineEntry(t *testing.T, baseline map[string]BaselineEntry, roots map[string]bool) {
	t.Helper()
	target, targetEntry, ok := coverageNonOriginalElevenTarget(baseline)
	if !ok {
		t.Fatal("coverage gate: no baseline entry outside the original 11 with enough floor headroom to seed a safe 1.0pt ratchet drop — run the coveragebaseline generator's --add-missing first")
	}
	droppedPct := targetEntry.Baseline - 1.0
	droppedProfile := map[string]*CoverageStats{
		target: {CoveredStmts: int(droppedPct * 100), TotalStmts: 10000},
	}
	dv := CheckCoverageWithRoots(droppedProfile, baseline, roots)
	for _, viol := range dv {
		if viol.Package == target {
			if viol.Reason != "ratchet" {
				t.Errorf("coverage gate: %s: expected reason \"ratchet\", got %q", target, viol.Reason)
			}
			return
		}
	}
	t.Fatalf("coverage gate: expected a ratchet violation for %s (baseline %.1f, dropped to %.1f), got %+v", target, targetEntry.Baseline, droppedPct, dv)
}

// coverageOriginalEleven names the 11 baseline entries this repo hand-
// curated before AUD-040's generator existed (coverage-baseline.json as
// of f688c0b) — see TestCoverageGate_SeededRatchetDropRed's extension,
// which must prove the ratchet on an entry OUTSIDE this set.
var coverageOriginalEleven = map[string]bool{
	"cmd/cascade": true, "internal/build": true, "internal/buildinfo": true,
	"internal/context/scope": true, "internal/output": true, "internal/plugins": true,
	"internal/retrieval/eval": true, "internal/runtime": true, "internal/storage/storetest": true,
	"internal/testkit": true, "providers/sqlite": true,
}

// coverageNonOriginalElevenTarget picks the baseline entry, outside
// coverageOriginalEleven, with the largest (baseline - floor) margin — the
// safest one to seed a synthetic 1.0-point drop against without the drop
// also tripping a FLOOR violation (which would make the test's "reason
// ratchet" assertion moot). ok is false if no entry has enough headroom.
func coverageNonOriginalElevenTarget(baseline map[string]BaselineEntry) (pkg string, entry BaselineEntry, ok bool) {
	bestMargin := -1.0
	for k, v := range baseline {
		if coverageOriginalEleven[k] {
			continue
		}
		margin := v.Baseline - v.Floor
		if margin > bestMargin {
			bestMargin, pkg, entry = margin, k, v
		}
	}
	return pkg, entry, bestMargin >= 1.05
}

// TestCoverageBaseline_TrackedFileIsCanonical proves the tracked baseline is
// a fixed point of the generator's only renderer, so an --add-missing run
// that adds nothing leaves the file byte-identical.
func TestCoverageBaseline_TrackedFileIsCanonical(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(coverageModuleRoot(t), "internal", "build", "testdata", "coverage-baseline.json"))
	if err != nil {
		t.Fatalf("coverage gate: reading baseline: %v", err)
	}
	entries, err := ParseBaseline(data)
	if err != nil {
		t.Fatalf("coverage gate: parsing baseline: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("coverage gate: baseline parsed to zero entries")
	}
	if got := FormatBaselineJSON(entries); string(got) != string(data) {
		t.Fatalf("coverage gate: tracked baseline is not canonical; re-render it with the generator (--add-missing)")
	}
}
