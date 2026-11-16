package learn

import (
	"reflect"
	"slices"
	"strconv"
	"testing"
)

func TestClassifyProposalTierTable(t *testing.T) {
	calls := 0
	orig := classifyChange
	classifyChange = func(target string, ch Change) (Classification, error) {
		calls++
		return orig(target, ch)
	}
	t.Cleanup(func() { classifyChange = orig })
	rules := ProposalRules()
	tiers, err := ClassifyProposalTierTable(rules)
	if err != nil {
		t.Fatal(err)
	}
	if calls != len(rules) || len(tiers) != len(rules) {
		t.Fatalf("calls=%d tiers=%d rules=%d", calls, len(tiers), len(rules))
	}
	if tiers[0] != TierBehavioral {
		t.Fatalf("ci add_step tier = %q, want behavioral", tiers[0])
	}
	bad := []ProposalRule{{Target: "ci.local_steps", Change: Change{Op: OpAddStep, Path: "ci.local.build", Value: `"rm -rf /"`}}}
	got, err := ClassifyProposalTierTable(bad)
	if err != nil || got[0] != TierSecurity {
		t.Fatalf("unknown step tier = %v, %v; want security", got, err)
	}
	for i := range reflect.TypeFor[Proposal]().NumField() {
		if reflect.TypeFor[Proposal]().Field(i).Type == reflect.TypeFor[ConfigTier]() {
			t.Fatalf("Proposal field %s lets a caller state a tier", reflect.TypeFor[Proposal]().Field(i).Name)
		}
	}
}

func TestProposalRuleGeneratedCodeToCIStep(t *testing.T) {
	rules := ProposalRules()
	if len(rules) != 1 {
		t.Fatalf("rules = %d, want 1", len(rules))
	}
	r := rules[0]
	want := Change{Op: OpAddStep, Path: "ci.local.build", Value: `"cascade context generate --check"`}
	if r.Family != FamilyToolFailure || r.Category != "*" || r.Component != "generated-code" ||
		r.Kind != "ci" || r.Target != "ci.local_steps" || r.Change != want {
		t.Fatalf("row = %+v", r)
	}
}

func TestProposalRuleStepValuesKnown(t *testing.T) {
	var known []string
	for _, v := range KnownStepValues() {
		known = append(known, strconv.Quote(v))
	}
	for _, r := range ProposalRules() {
		if r.Change.Op == OpAddStep && !slices.Contains(known, r.Change.Value) {
			t.Fatalf("rule value %q is outside KnownStepValues %q", r.Change.Value, known)
		}
	}
}
