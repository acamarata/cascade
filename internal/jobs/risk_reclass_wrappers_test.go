package jobs

// Purpose: closes the coverage gap on risk_reclass.go's EXPORTED
//   wrappers ClassifyFootprint/CriticalFloorMatch (the explain surface's
//   entry points), which had no test at all -- risk_test.go exercises
//   the unexported classifyFootprint/criticalFloorMatch directly, never
//   these wrappers. Feeds real footprints/paths that SHOULD and SHOULD
//   NOT match, including a Critical-floor case, so the assertion cuts
//   both ways.
// Inputs: nothing external.
// Outputs: n/a (test file).
// SPORT: jobs/risk-reclass-guard (FIX, coverage floor restoration).

import "testing"

// TestClassifyFootprint_Wrapper asserts the exported ClassifyFootprint
// agrees with classifyFootprint(footprint, singleRepository(), root)
// across a Critical, a High, a Low and a Normal case -- a wrapper only
// ever fed one matching input has not been tested.
func TestClassifyFootprint_Wrapper(t *testing.T) {
	cases := []struct {
		name      string
		footprint []string
		want      RiskClass
	}{
		{"critical: secrets path", []string{"internal/secrets/vault.go"}, RiskClassCritical},
		{"high: pkg path", []string{"pkg/provider/model.go"}, RiskClassHigh},
		{"low: docs only", []string{"docs/guide.md"}, RiskClassLow},
		{"normal: unrelated code path", []string{"internal/fleet/bench.go"}, RiskClassNormal},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ClassifyFootprint(c.footprint, "")
			if got != c.want {
				t.Fatalf("ClassifyFootprint(%v) = %v, want %v", c.footprint, got, c.want)
			}
			want := classifyFootprint(c.footprint, singleRepository(), "")
			if got != want {
				t.Fatalf("ClassifyFootprint(%v) = %v, disagrees with classifyFootprint's %v", c.footprint, got, want)
			}
		})
	}
}

// TestCriticalFloorMatch_Wrapper asserts CriticalFloorMatch reports the
// real category for a genuine floor hit AND reports no match at all for
// a path that is not one -- a matcher fed only matching input proves
// nothing about the negative case.
func TestCriticalFloorMatch_Wrapper(t *testing.T) {
	category, matched := CriticalFloorMatch("internal/jobs/risk.go")
	if !matched {
		t.Fatalf("CriticalFloorMatch(risk.go) matched = false, want true (classifier table floor)")
	}
	if category != string(floorClassifierTable) {
		t.Fatalf("CriticalFloorMatch(risk.go) category = %q, want %q", category, floorClassifierTable)
	}

	category, matched = CriticalFloorMatch("internal/secrets/egress.go")
	if !matched {
		t.Fatalf("CriticalFloorMatch(egress.go) matched = false, want true (egress-class floor)")
	}
	if category != string(floorEgressClass) {
		t.Fatalf("CriticalFloorMatch(egress.go) category = %q, want %q", category, floorEgressClass)
	}

	if category, matched := CriticalFloorMatch("internal/fleet/bench.go"); matched {
		t.Fatalf("CriticalFloorMatch(bench.go) = (%q, true), want (\"\", false): not a floor category", category)
	}
}
