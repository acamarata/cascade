package init

// Purpose: the policy preset step (P1-NODE-12c, 8c). It maps the init
//   [policy].preset shorthand onto the autonomy-profile API.
// Inputs: a preset name (balanced, strict or custom) and the running
//   *policy.Controller.
// Outputs: the installed *policy.AutonomyProfile.
// Constraints: there is no second profile table here. The preset is
//   handed to policy.Controller.Apply as [policy].autonomy_profile, so
//   the policy package alone decides what each name means. An unknown
//   preset is refused before the controller is touched, so it neither
//   swaps a profile nor emits a rejection event.
// SPORT: internal/runtime/init policy preset step (ADD) — P1-NODE-12c.

import (
	"context"
	"strings"

	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/runtime/initconfig"
	"github.com/acamarata/cascade/pkg/cascade"
)

// defaultPolicyPreset is the preset an unset [policy].preset selects: the
// autonomy baseline, which still asks at L2/L3 and denies at L4.
const defaultPolicyPreset = "balanced"

// policyPresetQuestion is the policy step's prompt wording.
const policyPresetQuestion = "Policy preset"

// ApplyPolicyPreset installs preset on ctrl through the autonomy-profile
// API and returns the profile now running.
//
// An empty preset is the default (balanced). A name outside
// policyPresets is KindInvalidInput, returned before ctrl
// is called, so the running profile stays exactly as it was.
func ApplyPolicyPreset(ctx context.Context, ctrl *policy.Controller, preset string) (*policy.AutonomyProfile, error) {
	name, err := resolvePolicyPreset(preset)
	if err != nil {
		return nil, err
	}
	if ctrl == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "cascade init: the policy step needs the autonomy controller")
	}
	tree := map[string]interface{}{
		"policy": map[string]interface{}{"autonomy_profile": name},
	}
	if err := ctrl.Apply(ctx, tree); err != nil {
		return nil, err
	}
	return ctrl.Profile(), nil
}

// policyPresets lists the presets initconfig accepts (initconfig/discovery.go validateDiscovery).
var policyPresets = []string{initconfig.PresetBalanced, initconfig.PresetStrict, initconfig.PresetCustom}

// resolvePolicyPreset maps an operator preset onto a profile name,
// refusing anything this build does not recognise.
func resolvePolicyPreset(preset string) (string, error) {
	name := strings.TrimSpace(preset)
	if name == "" {
		return defaultPolicyPreset, nil
	}
	for _, known := range policyPresets {
		if name == known {
			return name, nil
		}
	}
	return "", cascade.Newf(cascade.KindInvalidInput,
		"cascade init: %q is not a policy preset; choose one of %s",
		name, strings.Join(policyPresets, ", "))
}

// StepPolicy asks for the policy preset and applies it to ctrl.
//
// The question is answered by the wizard's Prompter, so --yes takes the
// balanced default and a setup file answers it the same way it answers
// every other question. A non-writing run validates the answer and plans
// the change without touching ctrl.
func (w *Wizard) StepPolicy(ctx context.Context, ctrl *policy.Controller) error {
	if w.deps.Prompt == nil {
		return cascade.New(cascade.KindInvalidInput, "cascade init: the policy step needs a prompter")
	}
	w.say("== policy")
	preset, err := w.deps.Prompt.Choose(policyPresetQuestion, policyPresets, defaultPolicyPreset)
	if err != nil {
		return err
	}
	name, err := resolvePolicyPreset(preset)
	if err != nil {
		return err
	}
	if !w.writing() {
		w.plan("apply policy preset %s", name)
		return nil
	}
	profile, err := ApplyPolicyPreset(ctx, ctrl, name)
	if err != nil {
		return err
	}
	w.say("   preset %s (autonomy profile %s)", name, profile.Name())
	return nil
}
