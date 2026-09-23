package jobs

// Purpose: AMD-20260922/F1-3 (R-14.303 item 3, register A1-136) --
//
//	proves the Low-risk fail-open defect completion_errors.go's
//	evidenceKindsForGateSet fix closes: before the fix, the whole Low
//	gate set (format, static, targeted_verification) had no EvidenceKind
//	mapping, so CompletionPolicy.Transition accepted a Low-risk job with
//	zero evidence rows -- a real fail-open security gap in the
//	completion gate, not merely an untested corner.
//
// Inputs: the real newCompletionFixture(t, RiskClassLow) rig
//
//	(completion_test.go) and the real GateSetForRiskClass table
//	(riskgates.go).
//
// Outputs: n/a (test file).
//
// Constraints: TestCompletionLowRiskRefusesWithoutEvidence's assertion
//
//	(MissingEvidence == exactly [lint, tests]) is REPRODUCED RED against
//	the pre-fix evidenceKindsForGateSet during this ticket's mutation
//	verification (see the build report) -- the pre-fix table returns an
//	EMPTY slice for Low, so Transition's completeness check is vacuously
//	satisfied and the Transition SUCCEEDS instead of denying.
//
// SPORT: jobs/completion-gate/FIX (AMD-20260922/F1-3, P1-E29-W6-S60-T4).

import (
	"context"
	"testing"
)

// TestCompletionLowRiskRefusesWithoutEvidence proves a Low-risk job with
// an empty evidence ledger is DENIED, not silently accepted: the fixed
// evidenceKindsForGateSet maps Low's whole gate set to exactly
// [EvidenceLint, EvidenceTests] (de-duplicated, first-seen order), so
// Transition's completeness check has something real to fail.
func TestCompletionLowRiskRefusesWithoutEvidence(t *testing.T) {
	cp, _, _, bus, job := newCompletionFixture(t, RiskClassLow)
	ctx := context.Background()
	// A docs/**-only footprint: classifyFootprint (risk.go) resolves an
	// EMPTY footprint to RiskClassNormal (the baseline default, never a
	// permissive Low), which would escalate a Low-planned job before
	// this test ever reaches the completeness check this test targets.
	// A real docs-only footprint keeps Reclassify's derived class at
	// Low, matching acceptance_path1_test.go's own pattern.
	err := cp.Transition(ctx, TransitionRequest{
		Job: &job, Target: JobStateVerifying, Caller: PolicyEngineIdentity{EngineID: testEngineID},
		PlannedRiskClass:   RiskClassLow,
		ActualFootprint:    ChangeFootprint{ChangedPaths: []string{"docs/fixture-a.md", "docs/fixture-b.md"}},
		LeaseScopePrefixes: []string{"docs/"},
	})
	denial, ok := err.(*ErrorCompletionDenied)
	if !ok {
		t.Fatalf("Transition(Low, no evidence) = %v, want *ErrorCompletionDenied (the pre-fix table let this through)", err)
	}
	if len(denial.MissingEvidence) != 2 || denial.MissingEvidence[0] != EvidenceLint || denial.MissingEvidence[1] != EvidenceTests {
		t.Fatalf("MissingEvidence = %v, want exactly [lint, tests]", denial.MissingEvidence)
	}
	if n := countGateEvents(t, bus); n != 1 {
		t.Fatalf("gate.denied events = %d, want exactly 1", n)
	}
}

// TestEvidenceKindsNonEmptyForEveryRiskClass proves the invariant
// AMD-20260922/F1-3 names: for each of the four RiskClass values the
// required EvidenceKind set is non-empty, and each class's set contains
// every EvidenceKind the classes below it require (GateSetForRiskClass's
// own additive gate-set structure carried through the mapping).
func TestEvidenceKindsNonEmptyForEveryRiskClass(t *testing.T) {
	classes := []RiskClass{RiskClassLow, RiskClassNormal, RiskClassHigh, RiskClassCritical}
	var prev map[EvidenceKind]bool
	for _, class := range classes {
		gates, err := GateSetForRiskClass(class)
		if err != nil {
			t.Fatalf("GateSetForRiskClass(%s): %v", class, err)
		}
		kinds := evidenceKindsForGateSet(gates)
		if len(kinds) == 0 {
			t.Fatalf("evidenceKindsForGateSet(%s) = empty, want every RiskClass to require at least one EvidenceKind", class)
		}
		cur := make(map[EvidenceKind]bool, len(kinds))
		for _, k := range kinds {
			cur[k] = true
		}
		for k := range prev {
			if !cur[k] {
				t.Errorf("%s's required evidence set %v drops %q, present in the lower class's set", class, kinds, k)
			}
		}
		prev = cur
	}
}
