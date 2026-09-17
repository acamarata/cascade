package init

// Purpose: the wizard's behavioural contract (P1-E16-W4-S35-T6) — the
//   three run modes, the journal-backed resume, the ratified flags, and
//   the refusals.
// Constraints: every assertion is about what a COLLABORATOR was asked to
//   do, not about the wizard's printed text, except where the text is the
//   deliverable (the step-9 summary).

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestWizardYesAllDefaults is the automation path: zero prompts, every
// default accepted, exit 0, and the machine actually set up.
//
// It asserts the real calls, not the transcript. A wizard that printed
// nine step headers and called nothing would produce identical output.
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

// TestWizardCheckDryRun: --check writes nothing at all — not the journal,
// not config, not a harness file — and still reports what a real run
// would change.
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

// TestWizardResumesFromTheJournal is the kill-and-relaunch contract: a
// run that stopped after step 5 does not re-do steps 1 to 5.
//
// The assertion is on which steps the second run PERFORMED, not on the
// final state — a wizard that re-ran everything would reach the same
// final state, which is exactly why that would be the wrong assertion.
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
	if len(report.Steps) != 4 {
		t.Errorf("the resumed run performed %v, want the four steps after providers", report.Steps)
	}
	if len(rec.providers) != 0 {
		t.Errorf("the resumed run re-ran provider intake: %v", rec.providers)
	}
	if got := report.State.Providers; len(got) != 1 || got[0] != "already-added" {
		t.Errorf("the resumed run lost the recorded providers: %v", got)
	}
}

// TestWizardJournalsAfterEveryStep proves the journal is written as the
// run goes, not once at the end — which is the whole contract, and the
// half a single end-of-run write would silently fail to honour.
func TestWizardJournalsAfterEveryStep(t *testing.T) {
	w, rec, _, home := fixture(t, Options{Mode: ModeYes}, nil)
	rec.doctorErr = nil
	// Fail at the last step by making the service install fail, which
	// stops the run at step 8 with steps 1-7 already done.
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

// TestWizardRefusesAHalfWiredBuild: a wizard missing a collaborator would
// skip that step silently and report a machine set up that is not.
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

// TestReconvergeRefusesWithoutAReadableMachine pins the rule that keeps a
// reconverge from degrading into a fresh run: converging against a state
// nobody could read would overwrite the configuration the operator asked
// to converge, which is the one outcome neither of them wanted.
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

// TestPreflightRefusesAnUnusableHome asserts the probe runs before any
// prompt: a wizard that asked nine questions and then failed on an
// unwritable home wasted the operator's time on a machine it could have
// refused immediately.
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

// TestCheckDoesNotCreateTheCascadeHome is the assertion the first real
// run of --check failed: the storage probe creates the home so a later
// step can write to it, and in a mode whose contract is "write nothing"
// creating a directory is a write.
//
// It asserts on the probe's own argument rather than on the filesystem,
// because the recorder cannot create anything — and the argument is what
// production's probe branches on.
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
