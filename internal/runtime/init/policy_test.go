package init

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestApplyPolicyPreset(t *testing.T) {
	cases := []struct {
		preset      string
		wantProfile string
		wantL1      policy.Verdict
		wantL3      policy.Verdict
		wantErrMsg  string
	}{
		{preset: "balanced", wantProfile: "balanced", wantL1: policy.VerdictAllow, wantL3: policy.VerdictAsk},
		{preset: "strict", wantProfile: "strict", wantL1: policy.VerdictAsk, wantL3: policy.VerdictDeny},
		{preset: "custom", wantProfile: "custom", wantL1: policy.VerdictAllow, wantL3: policy.VerdictAsk},
		{preset: "", wantProfile: "balanced", wantL1: policy.VerdictAllow, wantL3: policy.VerdictAsk},
		{preset: "permissive", wantErrMsg: `cascade init: "permissive" is not a policy preset; choose one of balanced, strict, custom`},
		{preset: "Strict", wantErrMsg: `cascade init: "Strict" is not a policy preset; choose one of balanced, strict, custom`},
	}
	for _, tc := range cases {
		t.Run("preset="+tc.preset, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("USERPROFILE", t.TempDir())
			events := []string{}
			ctrl := policy.NewController(func(_ context.Context, event string, _ map[string]interface{}) error {
				events = append(events, event)
				return nil
			})
			profile, err := ApplyPolicyPreset(context.Background(), ctrl, tc.preset)
			if tc.wantErrMsg != "" {
				assertRefusedBeforeMutation(t, err, tc.wantErrMsg, ctrl, events)
				return
			}
			if err != nil {
				t.Fatalf("ApplyPolicyPreset(%q): %v", tc.preset, err)
			}
			if profile != ctrl.Profile() || profile.Name() != tc.wantProfile {
				t.Fatalf("profile = %q (running %q), want %q", profile.Name(), ctrl.Profile().Name(), tc.wantProfile)
			}
			if got := profile.VerdictFor(policy.L1); got != tc.wantL1 {
				t.Fatalf("L1 = %v, want %v", got, tc.wantL1)
			}
			if got := profile.VerdictFor(policy.L3); got != tc.wantL3 {
				t.Fatalf("L3 = %v, want %v", got, tc.wantL3)
			}
			if len(events) != 1 || events[0] != policy.EventAutonomyProfileLoaded {
				t.Fatalf("events = %v, want one %s", events, policy.EventAutonomyProfileLoaded)
			}
		})
	}
	t.Run("step applies the chosen preset", testStepPolicyApplies)
	t.Run("check mode plans without mutation", testStepPolicyCheck)
}

// assertRefusedBeforeMutation checks the refusal by kind and message and
// that the controller was never reached: no profile, no event.
func assertRefusedBeforeMutation(t *testing.T, err error, msg string, ctrl *policy.Controller, events []string) {
	t.Helper()
	assertTaxonomyErr(t, err, cascade.KindInvalidInput, msg)
	if ctrl.Profile() != nil {
		t.Fatalf("a refused preset installed profile %q", ctrl.Profile().Name())
	}
	if len(events) != 0 {
		t.Fatalf("a refused preset reached the controller: events %v", events)
	}
}

func testStepPolicyApplies(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	prompt := &scriptedPrompter{choices: []string{"strict"}}
	w, _, out, _ := fixture(t, Options{Mode: ModeInteractive}, prompt)
	ctrl := policy.NewController(nil)
	if err := w.StepPolicy(context.Background(), ctrl); err != nil {
		t.Fatalf("StepPolicy: %v", err)
	}
	if ctrl.Profile().Name() != "strict" {
		t.Fatalf("running profile = %q, want strict", ctrl.Profile().Name())
	}
	if len(prompt.asked) != 1 || prompt.asked[0] != policyPresetQuestion {
		t.Fatalf("asked = %v, want one %q", prompt.asked, policyPresetQuestion)
	}
	outContains(t, out.String(), "preset strict (autonomy profile strict)", "policy output")
}

func testStepPolicyCheck(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	prompt := &scriptedPrompter{choices: []string{"custom"}}
	w, _, out, _ := fixture(t, Options{Mode: ModeCheck}, prompt)
	ctrl := policy.NewController(nil)
	if err := w.StepPolicy(context.Background(), ctrl); err != nil {
		t.Fatalf("StepPolicy: %v", err)
	}
	if ctrl.Profile() != nil {
		t.Fatalf("a check run installed profile %q", ctrl.Profile().Name())
	}
	outContains(t, out.String(), "would apply policy preset custom", "policy output")
}
