package init

// Purpose: the reconverge RUN (P1-E16-W4-S35-T7) -- the second `cascade
//   init --reconverge` end to end, over the recorded collaborators.
// Constraints: these assert on the ARGV the run produced, not on the
//   report it printed. Routing through `cascade config set` and
//   `cascade provider add` is the whole point of the design, and an
//   assertion on printed text would pass against a run that wrote the
//   config file itself.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/runtime"
)

// TestReconvergeAppliesThroughTheOwningSubcommands drives the whole
// second run and asserts on the ARGV, because the value of routing
// through `cascade config set` and `cascade provider add` is that those
// subcommands own the format and the credential path — an assertion on a
// recorded name would pass against an implementation that wrote the file
// itself.
func TestReconvergeAppliesThroughTheOwningSubcommands(t *testing.T) {
	w, rec, out, _ := fixture(t, Options{Mode: ModeYes, Reconverge: true}, nil)
	w.deps.Current = fakeCurrent{
		view: fakeConfigView{
			values:  map[string]any{"logging.level": "info", "retrieval.fusion": "off"},
			sources: map[string]runtime.ConfigSource{"retrieval.fusion": runtime.SourceFile},
		},
		providers: []string{"anthropic"},
		enabled:   []string{"pbd"},
		files: []HarnessFile{
			{Path: "/p/.claude/CLAUDE.md", Stale: true},
			{Path: "/p/AGENTS.md", Stale: true, Modified: true},
		},
	}
	w.opts.ReconvergeOptions = ReconvergeOptions{
		Desired:   map[string]any{"logging.level": "debug", "retrieval.fusion": "on"},
		Providers: []string{"anthropic", "openai"},
	}

	_, err := w.Run(context.Background())
	if !errors.Is(err, ErrReconvergeConflicts) {
		t.Fatalf("err = %v, want the exit-3 conflicts outcome", err)
	}

	calls := map[string]bool{}
	for _, c := range rec.subArgs {
		calls[strings.Join(c, " ")] = true
	}
	for _, want := range []string{
		"config set logging.level debug",
		"provider add openai --oauth",
		"provider test anthropic",
		"context harness sync",
	} {
		if !calls[want] {
			t.Errorf("`cascade %s` was never run; calls were %v", want, rec.subArgs)
		}
	}
	for _, unwanted := range []string{
		"config set retrieval.fusion on", // the user edited this
		"provider add anthropic --oauth", // already installed
	} {
		if calls[unwanted] {
			t.Errorf("`cascade %s` ran, reverting something it should have left alone", unwanted)
		}
	}
	if !strings.Contains(out.String(), "--force-section retrieval") {
		t.Errorf("the conflict does not tell the operator how to override it:\n%s", out)
	}
	if !strings.Contains(out.String(), "/p/AGENTS.md") {
		t.Errorf("the hand-edited file was not reported:\n%s", out)
	}
}

// TestACleanReconvergeExitsZero is the other half: a machine already at
// its desired state converges silently and succeeds.
func TestACleanReconvergeExitsZero(t *testing.T) {
	w, rec, out, _ := fixture(t, Options{Mode: ModeYes, Reconverge: true}, nil)
	w.deps.Current = fakeCurrent{
		view:      fakeConfigView{values: map[string]any{"logging.level": "debug"}},
		providers: []string{"anthropic"},
		enabled:   []string{"pbd"},
	}
	w.opts.ReconvergeOptions = ReconvergeOptions{
		Desired:   map[string]any{"logging.level": "debug"},
		Providers: []string{"anthropic"},
	}

	report, err := w.Run(context.Background())
	if err != nil {
		t.Fatalf("a converged machine reported %v", err)
	}
	if report.Converged == nil || !report.Converged.Clean() {
		t.Errorf("converged = %+v, want a clean convergence", report.Converged)
	}
	if !strings.Contains(out.String(), "already converged") {
		t.Errorf("a no-op reconverge printed nothing an operator can read:\n%s", out)
	}
	if len(rec.subArgs) != 1 || strings.Join(rec.subArgs[0], " ") != "provider test anthropic" {
		t.Errorf("a converged machine ran %v; only the re-verify should have happened", rec.subArgs)
	}
}

// TestAReconvergeThatCannotReadTheMachineChangesNothing: reading fails
// before anything is decided, so a half-read machine is never a
// half-converged one.
func TestAReconvergeThatCannotReadTheMachineChangesNothing(t *testing.T) {
	w, rec, _, _ := fixture(t, Options{Mode: ModeYes, Reconverge: true}, nil)
	w.deps.Current = fakeCurrent{viewErr: errors.New("config.toml is unreadable")}
	w.opts.ReconvergeOptions = ReconvergeOptions{Desired: map[string]any{"a.b": 1}}

	if _, err := w.Run(context.Background()); err == nil {
		t.Fatal("Run converged against a config it could not read")
	}
	if len(rec.subArgs) != 0 {
		t.Errorf("the failed read still ran %v", rec.subArgs)
	}
}

// TestACheckedReconvergeWritesNothing: --check is a property of every
// collaborator the run touches, including this one.
func TestACheckedReconvergeWritesNothing(t *testing.T) {
	w, rec, _, _ := fixture(t, Options{Mode: ModeCheck, Reconverge: true}, nil)
	w.deps.Current = fakeCurrent{
		view:  fakeConfigView{values: map[string]any{"logging.level": "info"}},
		files: []HarnessFile{{Path: "/p/x.md", Stale: true}},
	}
	w.opts.ReconvergeOptions = ReconvergeOptions{
		Desired:   map[string]any{"logging.level": "debug"},
		Providers: []string{"openai"},
	}

	report, err := w.Run(context.Background())
	if err != nil {
		t.Fatalf("a --check reconverge reported %v", err)
	}
	if len(rec.subArgs) != 0 {
		t.Errorf("--check ran %v", rec.subArgs)
	}
	if len(report.Diff) == 0 {
		t.Fatal("--check reported no plan; it cannot be distinguished from a no-op")
	}
	joined := strings.Join(report.Diff, "\n")
	for _, want := range []string{"set logging.level", "add provider openai", "regenerate /p/x.md"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the plan omits %q:\n%s", want, joined)
		}
	}
}

// TestAnUnsettableKeyIsReportedNotAttempted: `cascade config set` refuses
// a key its schema does not know, so attempting one would fail the whole
// converge over something that was never settable.
//
// Found for real: an earlier draft put telemetry.enabled in the desired
// map, and no such config key exists — every second run reported drift
// and would have failed on the apply.
func TestAnUnsettableKeyIsReportedNotAttempted(t *testing.T) {
	view := fakeConfigView{values: map[string]any{"logging.level": "info"}}

	got := MergeConfig(view, map[string]any{
		"logging.level":     "debug",
		"telemetry.enabled": false,
	}, nil)

	if len(got.Applied) != 1 || got.Applied[0] != "logging.level" {
		t.Errorf("applied %v, want only the key the config actually has", got.Applied)
	}
	if len(got.Unknown) != 1 || got.Unknown[0] != "telemetry.enabled" {
		t.Errorf("unknown = %v, want the key the config does not enumerate", got.Unknown)
	}
	if len(got.Conflicts) != 0 {
		t.Errorf("an unsettable key was reported as a user conflict: %+v", got.Conflicts)
	}
}

// TestAnUnsettableKeyIsNotSilentlyConverged: an unknown key means work
// this run could not do, so it must not also claim to be converged.
func TestAnUnsettableKeyIsNotSilentlyConverged(t *testing.T) {
	w, rec, out, _ := fixture(t, Options{Mode: ModeYes, Reconverge: true}, nil)
	w.deps.Current = fakeCurrent{view: fakeConfigView{values: map[string]any{"logging.level": "info"}}}
	w.opts.ReconvergeOptions = ReconvergeOptions{Desired: map[string]any{"nope.missing": true}}

	if _, err := w.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Contains(out.String(), "already converged") {
		t.Errorf("a run with an unsettable key claimed to be converged:\n%s", out)
	}
	if !strings.Contains(out.String(), "no such key") {
		t.Errorf("the unsettable key was not reported:\n%s", out)
	}
	for _, c := range rec.subArgs {
		if len(c) > 0 && c[0] == "config" {
			t.Errorf("the run attempted `cascade %v` for a key the config does not have", c)
		}
	}
}
