// Package policy (autonomy_owner_test.go): tests for the autonomous profile
// table and the subject-aware profile resolver.
package policy

import "testing"

// TestAutonomousProfileTable asserts the autonomous profile's verdict table:
// allow through L3, deny at L4.
func TestAutonomousProfileTable(t *testing.T) {
	p := autonomousProfile()
	if p == nil {
		t.Fatal("autonomousProfile returned nil")
	}
	if got := p.Name(); got != "autonomous" {
		t.Fatalf("Name() = %q, want %q", got, "autonomous")
	}

	cases := []struct {
		level RiskLevel
		want  Verdict
	}{
		{L0, VerdictAllow},
		{L1, VerdictAllow},
		{L2, VerdictAllow},
		{L3, VerdictAllow},
		{L4, VerdictDeny},
	}
	for _, tc := range cases {
		if got := p.VerdictFor(tc.level); got != tc.want {
			t.Errorf("VerdictFor(%v) = %v, want %v", tc.level, got, tc.want)
		}
	}
}

// TestProfileForAgentWithLiveApproval: an agent subject with live=true gets
// the autonomous table; with live=false it gets base.
func TestProfileForAgentWithLiveApproval(t *testing.T) {
	base := balancedProfile()
	agent := Subject{Kind: SubjectAgent, ID: "agent-1"}

	// live=true returns autonomous table
	p := ProfileFor(base, agent, true)
	if p == nil {
		t.Fatal("ProfileFor returned nil")
	}
	if got := p.Name(); got != "autonomous" {
		t.Errorf("ProfileFor(live=true).Name() = %q, want %q", got, "autonomous")
	}
	if got := p.VerdictFor(L2); got != VerdictAllow {
		t.Errorf("ProfileFor(live=true).VerdictFor(L2) = %v, want %v", got, VerdictAllow)
	}

	// live=false returns base unchanged
	p = ProfileFor(base, agent, false)
	if p != base {
		t.Error("ProfileFor(live=false) did not return base pointer")
	}
	if got := p.Name(); got != "balanced" {
		t.Errorf("ProfileFor(live=false).Name() = %q, want %q", got, "balanced")
	}
}

// TestProfileForOtherSubjectsKeepBase: user and plugin subjects and an
// invalid subject get base for both values of live.
func TestProfileForOtherSubjectsKeepBase(t *testing.T) {
	base := strictProfile()

	subjects := []Subject{
		{Kind: SubjectUser, ID: "alice"},
		{Kind: SubjectPlugin, ID: "plug-1"},
		{Kind: "bogus", ID: "x"},     // invalid kind
		{Kind: SubjectAgent, ID: ""}, // invalid: empty ID
	}

	for _, subj := range subjects {
		for _, live := range []bool{true, false} {
			p := ProfileFor(base, subj, live)
			if p != base {
				t.Errorf("ProfileFor(%+v, live=%v) did not return base pointer", subj, live)
			}
			if got := p.Name(); got != "strict" {
				t.Errorf("ProfileFor(%+v, live=%v).Name() = %q, want %q",
					subj, live, got, "strict")
			}
		}
	}
}

// TestConfigCannotNameAutonomous: parsing a [policy] section with
// autonomy_profile = "autonomous" is refused as an unknown profile.
func TestConfigCannotNameAutonomous(t *testing.T) {
	tree := policyTree(map[string]interface{}{"autonomy_profile": "autonomous"})
	cfg, err := ParseConfig(tree)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	_, err = Resolve(cfg)
	if err == nil {
		t.Fatal("Resolve accepted 'autonomous', want refusal")
	}
}
