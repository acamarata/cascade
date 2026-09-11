package jobs

import (
	"context"
	"testing"
)

// TestRiskReclassify_Escalation is the ticket-mandated case: a docs-planned
// job whose diff actually touches internal/policy escalates to
// Critical, naming the triggering path and the invalidated gate set.
func TestRiskReclassify_Escalation(t *testing.T) {
	derived, esc, outOfScope, err := Reclassify(context.Background(), RiskClassLow, ChangeFootprint{
		ChangedPaths: []string{"internal/policy/matrix.go"},
	}, []string{"internal/policy/"}, "")
	if err != nil {
		t.Fatalf("Reclassify() error = %v", err)
	}
	if derived != RiskClassCritical {
		t.Fatalf("derived = %v, want critical", derived)
	}
	if esc == nil {
		t.Fatal("esc = nil, want a non-nil Escalation")
	}
	if esc.From != RiskClassLow || esc.To != RiskClassCritical {
		t.Fatalf("esc = %+v, want From=low To=critical", esc)
	}
	if !gateSetHas(esc.InvalidatedGateSet, GateFormat) {
		t.Fatalf("InvalidatedGateSet must be the planned (low) gate set: %v", esc.InvalidatedGateSet)
	}
	if !gateSetHas(esc.RequiredGateSet, GateHumanApproval) {
		t.Fatalf("RequiredGateSet must be the derived (critical) gate set: %v", esc.RequiredGateSet)
	}
	found := false
	for _, p := range esc.TriggeringPaths {
		if p == "internal/policy/matrix.go" {
			found = true
		}
	}
	if !found {
		t.Fatalf("TriggeringPaths = %v, want internal/policy/matrix.go", esc.TriggeringPaths)
	}
	if len(outOfScope) != 0 {
		t.Fatalf("outOfScope = %v, want empty (path is covered by the lease prefix)", outOfScope)
	}
}

// TestRiskReclassify_NoDowngrade asserts a derived class BELOW planned
// returns the planned class unchanged, never a downgrade.
func TestRiskReclassify_NoDowngrade(t *testing.T) {
	derived, esc, _, err := Reclassify(context.Background(), RiskClassHigh, ChangeFootprint{
		ChangedPaths: []string{"docs/guide.md"},
	}, []string{"docs/"}, "")
	if err != nil {
		t.Fatalf("Reclassify() error = %v", err)
	}
	if derived != RiskClassHigh {
		t.Fatalf("derived = %v, want the planned class (high) unchanged, never downgraded to low", derived)
	}
	if esc != nil {
		t.Fatalf("esc = %+v, want nil (no escalation on a non-increase)", esc)
	}
}

// TestRiskReclassify_Unchanged asserts a derived class EQUAL to planned is
// reported as unchanged, with no escalation.
func TestRiskReclassify_Unchanged(t *testing.T) {
	derived, esc, _, err := Reclassify(context.Background(), RiskClassNormal, ChangeFootprint{
		ChangedPaths: []string{"internal/fleet/bench.go"},
	}, []string{"internal/fleet/"}, "")
	if err != nil {
		t.Fatalf("Reclassify() error = %v", err)
	}
	if derived != RiskClassNormal {
		t.Fatalf("derived = %v, want normal unchanged", derived)
	}
	if esc != nil {
		t.Fatalf("esc = %+v, want nil", esc)
	}
}

// TestScopeContainment_ViolationNamesOffendingPaths asserts a non-empty
// out-of-scope set names every changed path outside the lease's
// normalized scope prefixes, independent of the derived risk class.
func TestScopeContainment_ViolationNamesOffendingPaths(t *testing.T) {
	derived, _, outOfScope, err := Reclassify(context.Background(), RiskClassNormal, ChangeFootprint{
		ChangedPaths: []string{"internal/fleet/bench.go", "internal/other/unexpected.go"},
	}, []string{"internal/fleet/"}, "")
	if err != nil {
		t.Fatalf("Reclassify() error = %v", err)
	}
	if derived != RiskClassNormal {
		t.Fatalf("derived = %v, want normal (containment is independent of class)", derived)
	}
	if len(outOfScope) != 1 || outOfScope[0] != "internal/other/unexpected.go" {
		t.Fatalf("outOfScope = %v, want exactly [internal/other/unexpected.go]", outOfScope)
	}
}

func TestRiskReclassify_DependencyImpactPathsJoinTheUnion(t *testing.T) {
	derived, esc, _, err := Reclassify(context.Background(), RiskClassNormal, ChangeFootprint{
		ChangedPaths:          []string{"docs/guide.md"},
		DependencyImpactPaths: []string{"internal/secrets/vault.go"},
	}, []string{"docs/", "internal/secrets/"}, "")
	if err != nil {
		t.Fatalf("Reclassify() error = %v", err)
	}
	if derived != RiskClassCritical {
		t.Fatalf("derived = %v, want critical: dependency-impact paths must join the reclassification union", derived)
	}
	if esc == nil {
		t.Fatal("esc = nil, want an escalation from the dependency-impact path")
	}
}
