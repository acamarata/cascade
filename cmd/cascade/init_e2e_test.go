package main

// Purpose: `cascade init` driven END TO END through the real root command
//   (P1-E16-W4-S35-T6): a --check run over an unconfigured machine, a
//   --yes run that sets one up, a resumed run, and the adapters that
//   reach outside this process.
// Constraints: Art.7.1 — HOME, CASCADE_HOME and the working directory are
//   all temp trees, and --no-daemon keeps the suite from installing a
//   launchd or systemd unit on the machine running it. Split from
//   init_cmd_test.go for Art.10.3's 300-line cap.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	cascaderuntime "github.com/acamarata/cascade/internal/runtime"
	cascadeinit "github.com/acamarata/cascade/internal/runtime/init"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestInitCheckRunsEndToEndAndWritesNothing drives `cascade init --check`
// through the REAL root command against a temp home: the real detector,
// the real plugin registry, the real storage probe and the real doctor
// registry, with nothing stubbed.
//
// It is the acceptance shape of this ticket in one test. Two assertions
// carry it: exit 3 on a machine that is not set up, and a home directory
// that does not exist afterwards — a --check that created `~/.cascade` to
// prove it could have is the defect this caught the first time it ran for
// real.
func TestInitCheckRunsEndToEndAndWritesNothing(t *testing.T) {
	home := t.TempDir()
	cascadeHome := filepath.Join(home, ".cascade")
	t.Setenv("HOME", home)
	t.Setenv("CASCADE_HOME", cascadeHome)
	t.Chdir(t.TempDir())

	out, err := execRoot(t, "init", "--check")
	if err == nil {
		t.Fatal("a --check run over an unconfigured machine reported nothing to do")
	}
	kind, ok := cascade.KindOf(err)
	if !ok || kind.ExitCode() != 3 {
		t.Errorf("err = %v (kind %v), want the ratified exit 3", err, kind)
	}
	if _, statErr := os.Stat(cascadeHome); !os.IsNotExist(statErr) {
		t.Errorf("--check created %s (stat err = %v)", cascadeHome, statErr)
	}
	for _, want := range []string{"preflight", "plugins", "harnesses", "would", "cascade is set up"} {
		if !strings.Contains(out, want) {
			t.Errorf("the --check transcript omits %q:\n%s", want, out)
		}
	}
}

// TestInitYesSetsTheMachineUp is the same run in the mode automation
// uses: it writes, it finishes, and it leaves no journal behind.
//
// --no-daemon so the test never installs a launchd or systemd unit on the
// machine running it (Art.7.1).
func TestInitYesSetsTheMachineUp(t *testing.T) {
	home := t.TempDir()
	cascadeHome := filepath.Join(home, ".cascade")
	t.Setenv("HOME", home)
	t.Setenv("CASCADE_HOME", cascadeHome)
	t.Chdir(t.TempDir())

	out, err := execRoot(t, "init", "--yes", "--no-daemon")
	if err != nil {
		t.Fatalf("init --yes: %v\n%s", err, out)
	}
	if _, statErr := os.Stat(cascadeHome); statErr != nil {
		t.Errorf("a writing run did not create %s: %v", cascadeHome, statErr)
	}
	journal := filepath.Join(cascadeHome, cascadeinit.StateFileName)
	if _, statErr := os.Stat(journal); !os.IsNotExist(statErr) {
		t.Error("a completed run left its journal behind; the next run would report a resume with nothing to resume")
	}
	if !strings.Contains(out, "not installed (--no-daemon)") {
		t.Errorf("the summary does not say why the daemon was skipped:\n%s", out)
	}
}

// TestInitResumesAfterAKill is the journal's whole point, driven through
// the real command: plant a journal recording five completed steps, run
// again, and require the four remaining steps only.
func TestInitResumesAfterAKill(t *testing.T) {
	home := t.TempDir()
	cascadeHome := filepath.Join(home, ".cascade")
	t.Setenv("HOME", home)
	t.Setenv("CASCADE_HOME", cascadeHome)
	t.Chdir(t.TempDir())

	if err := cascadeinit.SaveState(cascadeHome, cascadeinit.State{
		CompletedStep: cascadeinit.StepProviders,
		Profile:       cascadeinit.ProfileLocal,
		Providers:     []string{"already-added"},
	}); err != nil {
		t.Fatalf("planting a journal: %v", err)
	}

	out, err := execRoot(t, "init", "--yes", "--no-daemon")
	if err != nil {
		t.Fatalf("resumed init: %v\n%s", err, out)
	}
	for _, redone := range []string{"== preflight", "== profile", "== storage", "== plugins", "== providers"} {
		if strings.Contains(out, redone) {
			t.Errorf("the resumed run re-ran %q:\n%s", redone, out)
		}
	}
	if !strings.Contains(out, "== harnesses") {
		t.Errorf("the resumed run did not continue from the first incomplete step:\n%s", out)
	}
	if !strings.Contains(out, "already-added") {
		t.Errorf("the summary lost what the earlier run recorded:\n%s", out)
	}
}

// TestProductionDepsAreFullyWired asserts every collaborator the wizard
// needs is supplied. The wizard itself refuses on a nil, so this is the
// test that turns that refusal from a runtime surprise into a build-time
// one.
func TestProductionDepsAreFullyWired(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())
	paths, err := cascaderuntime.NewPathProvider(nil, nil)
	if err != nil {
		t.Fatalf("NewPathProvider: %v", err)
	}
	cmd := newInitCmd()
	cmd.SetOut(os.NewFile(0, os.DevNull))

	deps, err := productionInitDeps(cmd, paths)
	if err != nil {
		t.Fatalf("productionInitDeps: %v", err)
	}
	for name, present := range map[string]bool{
		"Home": deps.Home != "", "Cwd": deps.Cwd != "", "GOOS": deps.GOOS != "",
		"Out": deps.Out != nil, "Prompt": deps.Prompt != nil, "Detector": deps.Detector != nil,
		"Wirer": deps.Wirer != nil, "Catalog": deps.Catalog != nil, "Service": deps.Service != nil,
		"Enroller": deps.Enroller != nil, "Doctor": deps.Doctor != nil, "Sub": deps.Sub != nil,
		"Storage": deps.Storage != nil, "Secrets": deps.Secrets != nil,
	} {
		if !present {
			t.Errorf("production wiring leaves %s unset; its step would be skipped", name)
		}
	}
}

// TestThePrompterMatchesTheMode: a non-interactive run must never reach
// for a terminal, because in CI there is not one and the run would hang.
func TestThePrompterMatchesTheMode(t *testing.T) {
	cmd := newInitCmd()
	for name, tc := range map[string]struct {
		flags       initFlags
		wantDefault bool
	}{
		"--yes":       {initFlags{yes: true}, true},
		"--check":     {initFlags{check: true}, true},
		"interactive": {initFlags{}, false},
	} {
		t.Run(name, func(t *testing.T) {
			_, isDefault := initPrompter(cmd, &tc.flags).(cascadeinit.DefaultPrompter)
			if isDefault != tc.wantDefault {
				t.Errorf("default prompter = %v, want %v", isDefault, tc.wantDefault)
			}
		})
	}
}

// TestTheFirstRunDoctorRunsRealChecks: the wizard's last step must report
// on the machine, not print a heading. A registry with no first-run
// checks is a refusal, because a doctor that examined nothing rendering
// as healthy is the Article-1 failure this step is most exposed to.
func TestTheFirstRunDoctorRunsRealChecks(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	paths, err := cascaderuntime.NewPathProvider(nil, nil)
	if err != nil {
		t.Fatalf("NewPathProvider: %v", err)
	}

	summary, err := initDoctorRunner{paths: paths}.FirstRun(context.Background())
	if err != nil {
		t.Fatalf("FirstRun: %v", err)
	}
	if strings.TrimSpace(summary) == "" {
		t.Fatal("the first-run health check rendered nothing")
	}
	if !strings.Contains(summary, "ok") && !strings.Contains(summary, "warn") && !strings.Contains(summary, "error") {
		t.Errorf("the summary carries no verdicts:\n%s", summary)
	}
}

// TestInitCwdResolvesTheProjectDirectory covers the one-line helper the
// harness step writes instruction files relative to.
func TestInitCwdResolvesTheProjectDirectory(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	got, err := initCwd()
	if err != nil {
		t.Fatalf("initCwd: %v", err)
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	if gotResolved, evalErr := filepath.EvalSymlinks(got); evalErr != nil || gotResolved != resolved {
		t.Errorf("initCwd = %q, want %q", got, resolved)
	}
}
