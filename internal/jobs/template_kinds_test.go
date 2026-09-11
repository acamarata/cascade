package jobs

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/conductor"
)

// NOTE on the K/S-23.T6 "FlowDecision" acceptance criterion: K/S-23.T6
// (internal/conductor/flow.go) ships ParseVerdict, Consensus and
// LoopStop -- three pure functions over MULTI-REVIEWER verdict sets
// collected at review-decision time. None of the three produces a gate
// set, and DagNode carries no gate-set field (S-59.T4's frozen field
// set, reasserted by this ticket's own full_desc: "dag.go gains no
// field from this ticket"). There is therefore no real call this
// ticket could make from a single-node, plan-time Resolve into any of
// the three that would not be fabricated wiring -- Consensus needs a
// ConsensusInput of collected reviewer verdicts that do not exist until
// review actually runs, and calling it with synthetic single-entry
// input here would be exactly the "check that shares the bug it
// checks for" anti-pattern the phase has already hit four times. This
// file therefore tests risk_class/gate-set behavior via the real
// AC/S-59.T4 classifier (FootprintClassifier) instead, which IS the
// mechanism ImplementTemplate/IntegrateTemplate actually use, and
// leaves the K/S-23.T6 gap disclosed in the journal rather than
// papered over here.

func withTC(tc TemplateContext) context.Context {
	return WithTemplateContext(context.Background(), tc)
}

func TestImplementTemplate_Resolve(t *testing.T) {
	tmpl := NewImplementTemplate(func(footprint []string) RiskClass {
		if len(footprint) > 0 && footprint[0] == "internal/secrets/x.go" {
			return RiskClassCritical
		}
		return RiskClassNormal
	})
	node, err := tmpl.Resolve(withTC(TemplateContext{
		ID:        "t-1",
		Footprint: []string{"internal/secrets/x.go"},
		DependsOn: []string{"t-0"},
	}))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if node.ID != "t-1" {
		t.Errorf("ID = %q, want t-1", node.ID)
	}
	if len(node.MutableScope) != 1 || node.MutableScope[0] != "internal/secrets/x.go" {
		t.Errorf("MutableScope = %v, want the ticket footprint", node.MutableScope)
	}
	if node.MinTaskClass != conductor.TaskClassCode {
		t.Errorf("MinTaskClass = %q, want code", node.MinTaskClass)
	}
	if node.RiskClass != RiskClassCritical {
		t.Errorf("RiskClass = %q, want critical from the injected classifier", node.RiskClass)
	}
	if len(node.Deps) != 1 || node.Deps[0] != "t-0" {
		t.Errorf("Deps = %v, want [t-0]", node.Deps)
	}
}

func TestReviewTemplate_Resolve(t *testing.T) {
	node, err := (ReviewTemplate{}).Resolve(withTC(TemplateContext{
		ID:        "t-2",
		Footprint: []string{"should/be/dropped.go"},
	}))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(node.MutableScope) != 0 {
		t.Errorf("MutableScope = %v, want empty (read-only kind)", node.MutableScope)
	}
	if node.MinTaskClass != conductor.TaskClassReview {
		t.Errorf("MinTaskClass = %q, want review", node.MinTaskClass)
	}
	wantCaps := map[string]bool{"review": true, "family:distinct-from-author": true}
	if len(node.Capabilities) != len(wantCaps) {
		t.Fatalf("Capabilities = %v, want %v", node.Capabilities, wantCaps)
	}
	for _, c := range node.Capabilities {
		if !wantCaps[c] {
			t.Errorf("unexpected capability %q", c)
		}
	}
}

func TestAdversarialTemplate_Resolve(t *testing.T) {
	node, err := (AdversarialTemplate{}).Resolve(withTC(TemplateContext{ID: "t-3"}))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(node.MutableScope) != 0 {
		t.Errorf("MutableScope = %v, want empty", node.MutableScope)
	}
	if node.MinTaskClass != conductor.TaskClassReview {
		t.Errorf("MinTaskClass = %q, want review", node.MinTaskClass)
	}
	wantCaps := map[string]bool{"review": true, "adversarial": true}
	if len(node.Capabilities) != len(wantCaps) {
		t.Fatalf("Capabilities = %v, want %v", node.Capabilities, wantCaps)
	}
}

func TestQaTemplate_Resolve(t *testing.T) {
	node, err := (QaTemplate{}).Resolve(withTC(TemplateContext{ID: "t-4"}))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(node.MutableScope) != 0 {
		t.Errorf("MutableScope = %v, want empty", node.MutableScope)
	}
	if node.MinTaskClass != conductor.TaskClassReview {
		t.Errorf("MinTaskClass = %q, want review", node.MinTaskClass)
	}
	if len(node.Capabilities) != 0 {
		t.Errorf("Capabilities = %v, want none beyond the caller's own (empty here)", node.Capabilities)
	}
}

func TestCiTemplate_Resolve(t *testing.T) {
	node, err := (CiTemplate{}).Resolve(withTC(TemplateContext{ID: "t-5"}))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(node.MutableScope) != 0 {
		t.Errorf("MutableScope = %v, want empty", node.MutableScope)
	}
	if node.MinTaskClass != conductor.TaskClassCode {
		t.Errorf("MinTaskClass = %q, want code", node.MinTaskClass)
	}
	if node.NodeRequirements[ciCleanRoomKey] != "true" {
		t.Errorf("NodeRequirements[%q] = %q, want true", ciCleanRoomKey, node.NodeRequirements[ciCleanRoomKey])
	}
}

func TestCiTemplate_Resolve_PreservesCallerNodeRequirements(t *testing.T) {
	node, err := (CiTemplate{}).Resolve(withTC(TemplateContext{
		ID: "t-5b",
		PassThroughFields: PassThroughFields{
			NodeRequirements: map[string]string{"gpu": "true"},
		},
	}))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if node.NodeRequirements["gpu"] != "true" || node.NodeRequirements[ciCleanRoomKey] != "true" {
		t.Errorf("NodeRequirements = %v, want both gpu and clean-room", node.NodeRequirements)
	}
}

func TestIntegrateTemplate_Resolve_DefaultNormal(t *testing.T) {
	tmpl := NewIntegrateTemplate(func([]string) RiskClass { return RiskClassLow })
	node, err := tmpl.Resolve(withTC(TemplateContext{ID: "t-6", Footprint: []string{"docs/x.md"}}))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if node.RiskClass != RiskClassNormal {
		t.Errorf("RiskClass = %q, want the Normal floor (classifier returned a lower class)", node.RiskClass)
	}
	if len(node.MutableScope) != 1 || node.MutableScope[0] != "docs/x.md" {
		t.Errorf("MutableScope = %v, want the target subtree glob", node.MutableScope)
	}
}

func TestIntegrateTemplate_Resolve_ClassifierElevates(t *testing.T) {
	tmpl := NewIntegrateTemplate(func([]string) RiskClass { return RiskClassCritical })
	node, err := tmpl.Resolve(withTC(TemplateContext{ID: "t-7", Footprint: []string{"internal/policy/x.go"}}))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if node.RiskClass != RiskClassCritical {
		t.Errorf("RiskClass = %q, want critical (classifier elevated above the Normal default)", node.RiskClass)
	}
}

func TestIntegrateTemplate_Resolve_DefaultClassifierIsProduction(t *testing.T) {
	tmpl := NewIntegrateTemplate(nil)
	node, err := tmpl.Resolve(withTC(TemplateContext{ID: "t-8", Footprint: []string{"internal/secrets/x.go"}}))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if node.RiskClass != RiskClassCritical {
		t.Errorf("RiskClass = %q, want critical from the real S-59.T4 classifier over internal/secrets/", node.RiskClass)
	}
}

// TestDefaultClassifier_MatchesProductionClassifier proves
// defaultClassifier really is S-59.T4's own classifyFootprint and not
// a parallel re-derivation: both must classify the same footprint
// identically for every RiskClass boundary case risk_test.go already
// exercises.
func TestDefaultClassifier_MatchesProductionClassifier(t *testing.T) {
	cases := [][]string{
		{"internal/secrets/x.go"},
		{"internal/policy/y.go"},
		nil,
		{"docs/readme.md"},
		{"pkg/foo/bar.go"},
	}
	for _, footprint := range cases {
		got := defaultClassifier(footprint)
		want := classifyFootprint(footprint, singleRepository(), "")
		if got != want {
			t.Errorf("defaultClassifier(%v) = %q, want %q (classifyFootprint)", footprint, got, want)
		}
	}
}
