package init

// Purpose: the wizard's behavioural contract (P1-E16-W4-S35-T6):
//   run modes, resume, flags, refusals, the question budget and the canary.
// Constraints: assertions are on COLLABORATOR calls, not printed text,
//   except where the text is the deliverable (the summary, doctor notes).

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/runtime/initconfig"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestWizardYesAllDefaults: --yes takes every default with zero prompts and
// the machine is actually set up (real calls, not a transcript).
func TestWizardYesAllDefaults(t *testing.T) {
	w, rec, out, home := fixture(t, Options{Mode: ModeYes}, nil)

	report, err := w.Run(context.Background())
	if err != nil {
		t.Fatalf("Run(--yes): %v", err)
	}
	if !report.State.Complete() {
		t.Fatalf("a --yes run stopped at step %v", report.State.CompletedStep)
	}
	if report.Resumed {
		t.Error("a fresh run reported itself as resumed")
	}
	if len(rec.wired) != 2 {
		t.Errorf("wired %v, want both detected harnesses", rec.wired)
	}
	if !rec.installed {
		t.Error("the daemon service was never installed")
	}
	if !rec.enrolled {
		t.Error("the elevation helper was never enrolled")
	}
	if !rec.doctorRan {
		t.Error("the first-run health check never ran")
	}
	if report.State.Telemetry {
		t.Error("telemetry defaulted ON; a mode that answers for the operator must not opt them in")
	}
	if len(rec.providers) != 0 {
		t.Errorf("--yes added providers %v; it has no way to obtain a credential", rec.providers)
	}
	if _, statErr := os.Stat(StatePath(home)); !os.IsNotExist(statErr) {
		t.Error("a completed run left its journal behind; the next run would report a resume with nothing to resume")
	}
	if !strings.Contains(out.String(), "cascade is set up") {
		t.Errorf("no summary card was rendered:\n%s", out)
	}
	if !strings.Contains(out.String(), rec.fingerprint) {
		t.Errorf("the summary card omits the enrolled helper fingerprint:\n%s", out)
	}
}

// TestWizardCheckDryRun: --check writes nothing and still reports what a
// real run would change.
func TestWizardCheckDryRun(t *testing.T) {
	w, rec, out, home := fixture(t, Options{Mode: ModeCheck}, nil)

	report, err := w.Run(context.Background())
	if err != nil {
		t.Fatalf("Run(--check): %v", err)
	}
	for name, happened := range map[string]bool{
		"harnesses were wired":     len(rec.wired) > 0,
		"the daemon was installed": rec.installed,
		"the helper was enrolled":  rec.enrolled,
		"doctor was run":           rec.doctorRan,
	} {
		if happened {
			t.Errorf("--check changed the machine: %s", name)
		}
	}
	if _, statErr := os.Stat(StatePath(home)); !os.IsNotExist(statErr) {
		t.Error("--check wrote a journal")
	}
	if len(report.Diff) == 0 {
		t.Fatal("--check reported no diff; it cannot be distinguished from a no-op")
	}
	joined := strings.Join(report.Diff, "\n")
	for _, want := range []string{"wire claude", "install the cascade daemon"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the diff omits %q:\n%s", want, joined)
		}
	}
	if !strings.Contains(out.String(), "would wire claude") {
		t.Errorf("--check printed no plan for a terminal reader:\n%s", out)
	}
}

// TestWizardResumesFromTheJournal: a run that stopped after step 5 does not
// re-do steps 1 to 5 — asserted on the steps PERFORMED, not the end state.
func TestWizardResumesFromTheJournal(t *testing.T) {
	w, rec, _, home := fixture(t, Options{Mode: ModeYes}, nil)
	if err := SaveState(home, State{
		CompletedStep: StepProviders, Profile: ProfileLocal,
		StoragePath: "/db", Providers: []string{"already-added"},
	}); err != nil {
		t.Fatalf("planting a journal: %v", err)
	}

	report, err := w.Run(context.Background())
	if err != nil {
		t.Fatalf("Run on a resumed machine: %v", err)
	}
	if !report.Resumed {
		t.Error("Resumed = false with a journal on disk")
	}
	for _, redone := range []string{"preflight", "profile", "storage", "plugins", "providers"} {
		for _, ran := range report.Steps {
			if ran == redone {
				t.Errorf("the resumed run re-ran step %q", redone)
			}
		}
	}
	if len(report.Steps) != 6 {
		t.Errorf("the resumed run performed %v, want the six steps after providers", report.Steps)
	}
	if len(rec.providers) != 0 {
		t.Errorf("the resumed run re-ran provider intake: %v", rec.providers)
	}
	if got := report.State.Providers; len(got) != 1 || got[0] != "already-added" {
		t.Errorf("the resumed run lost the recorded providers: %v", got)
	}
}

// TestWizardJournalsAfterEveryStep: the journal is written as the run goes,
// not once at the end.
func TestWizardJournalsAfterEveryStep(t *testing.T) {
	w, rec, _, home := fixture(t, Options{Mode: ModeYes}, nil)
	rec.doctorErr = nil
	// A failing service install stops the run at step 8, after 1-7.
	rec.installErr = cascade.New(cascade.KindUnavailable, "no service manager")

	if _, err := w.Run(context.Background()); err == nil {
		t.Fatal("Run succeeded with a failing service install")
	}
	state, found, err := LoadState(home)
	if err != nil {
		t.Fatalf("LoadState after a failed run: %v", err)
	}
	if !found {
		t.Fatal("a run that failed at step 8 left no journal; the next run would start over")
	}
	if state.CompletedStep != StepTelemetry {
		t.Errorf("the journal records step %v, want the last step that actually finished (%v)",
			state.CompletedStep, StepTelemetry)
	}
	if len(state.Harnesses) == 0 {
		t.Error("the journal lost step 6's result, so a resume would not know what was wired")
	}
}

// TestWizardRefusesAHalfWiredBuild: a missing collaborator is a refusal,
// never a silently skipped step.
func TestWizardRefusesAHalfWiredBuild(t *testing.T) {
	w, _, _, home := fixture(t, Options{Mode: ModeYes}, nil)
	w.deps.Detector = nil

	_, err := w.Run(context.Background())
	if err == nil {
		t.Fatal("Run succeeded with no harness detector wired")
	}
	if !strings.Contains(err.Error(), "Detector") {
		t.Errorf("the refusal does not name what is missing: %v", err)
	}
	if _, statErr := os.Stat(StatePath(home)); !os.IsNotExist(statErr) {
		t.Error("the refusal still wrote a journal")
	}
}

// TestReconvergeRefusesWithoutAReadableMachine: converging against a state
// nobody could read would overwrite the configuration it meant to converge.
func TestReconvergeRefusesWithoutAReadableMachine(t *testing.T) {
	w, rec, _, _ := fixture(t, Options{Mode: ModeYes, Reconverge: true}, nil)
	w.deps.Current = nil

	_, err := w.Run(context.Background())
	if err == nil {
		t.Fatal("Run converged against a machine it could not read")
	}
	if !errors.Is(err, ErrReconvergeUnavailable) {
		t.Errorf("err = %v, want ErrReconvergeUnavailable", err)
	}
	if rec.installed || len(rec.wired) > 0 {
		t.Error("the refusal still changed the machine")
	}
}

// TestPreflightRefusesAnUnusableHome: the home is probed before any
// question is asked.
func TestPreflightRefusesAnUnusableHome(t *testing.T) {
	prompt := &scriptedPrompter{}
	w, rec, _, _ := fixture(t, Options{}, prompt)
	rec.probeErr = cascade.New(cascade.KindUnavailable, "read-only filesystem")

	if _, err := w.Run(context.Background()); err == nil {
		t.Fatal("Run proceeded over an unusable cascade home")
	}
	if len(prompt.asked) != 0 {
		t.Errorf("the wizard asked %d question(s) before probing the home: %v", len(prompt.asked), prompt.asked)
	}
}

// TestCheckDoesNotCreateTheCascadeHome: under --check the probe may not
// create the home, asserted on the argument production branches on.
func TestCheckDoesNotCreateTheCascadeHome(t *testing.T) {
	for name, tc := range map[string]struct {
		mode Mode
		want bool
	}{
		"--check may not create": {ModeCheck, false},
		"--yes may create":       {ModeYes, true},
	} {
		t.Run(name, func(t *testing.T) {
			w, rec, _, _ := fixture(t, Options{Mode: tc.mode}, nil)
			if _, err := w.Run(context.Background()); err != nil {
				t.Fatalf("Run: %v", err)
			}
			if len(rec.probedCreate) != 1 {
				t.Fatalf("the home was probed %d time(s), want once", len(rec.probedCreate))
			}
			if rec.probedCreate[0] != tc.want {
				t.Errorf("probed with mayCreate = %v, want %v", rec.probedCreate[0], tc.want)
			}
		})
	}
}

// v2SetupFile answers every V2 site: a provider by key_env, a node by
// CASCADE_INIT_NODE_CODE_*, and the policy preset.
const v2SetupFile = completeSetupFile + `[[providers]]
name = "work"
auth = "key-env"
key_env = "INIT_CANARY_KEY"
[nodes]
pair = ["n1.invalid"]
[policy]
preset = "%s"
`

// v2Canary is the value both env references resolve to; it must never
// leave the process that reads it.
const v2Canary = "canary-value-7c1e0b"

// v2Run builds a spec-driven wizard over v2SetupFile. Its prompter is the
// spec's own with CASCADE_NO_INPUT forced on, so every would-be prompt is
// recorded as a refusal at the Prompter boundary and fails the run.
func v2Run(t *testing.T, flags initconfig.Flags, env map[string]string, preset string) (*Wizard, *recorder, string) {
	t.Helper()
	isolateHome(t)
	env["INIT_CANARY_KEY"], env[initconfig.NodeCodeEnv("n1.invalid")] = v2Canary, v2Canary
	body := strings.Replace(v2SetupFile, "%s", preset, 1)
	spec := specFor(t, flags, env, body)
	strict := *spec
	strict.Yes, strict.NoInput = false, true
	mode := map[bool]Mode{false: ModeInteractive, true: ModeYes}[flags.Yes]
	w, rec, _, _ := fixture(t, Options{Mode: mode, Spec: spec}, NewSpecPrompter(&strict))
	rec.env, w.deps.Pairer = env, &fakePairer{}
	return w, rec, body
}

// mustRun runs w, failing the test on an error.
func mustRun(t *testing.T, w *Wizard) Report {
	t.Helper()
	report, err := w.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return report
}

// TestWizardV2QuestionBudget: a typical machine asks at most five
// questions; --config, --yes and CASCADE_NO_INPUT=1 over a complete spec
// ask none.
func TestWizardV2QuestionBudget(t *testing.T) {
	w, prompt, _ := typicalMachine(t)
	if mustRun(t, w); len(prompt.asked) > 5 {
		t.Errorf("a typical machine was asked %d questions, want <= 5: %v", len(prompt.asked), prompt.asked)
	}
	for name, yes := range map[string]bool{"--config": false, "--yes": true, "CASCADE_NO_INPUT=1": false} {
		env := map[string]string{}
		if name == "CASCADE_NO_INPUT=1" {
			env[initconfig.EnvNoInput] = "1"
		}
		w, _, _ := v2Run(t, initconfig.Flags{Yes: yes}, env, "balanced")
		if report, err := w.Run(context.Background()); err != nil || !report.State.Complete() {
			t.Errorf("%s over a complete spec: a prompt was recorded or the run stopped: %v", name, err)
		}
	}
}
