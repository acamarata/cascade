package init

// Purpose: steps 2-7's own contracts (P1-E16-W4-S35-T6): the profile
//   flag and the worker handoff, the literal-secret refusal, the catalog,
//   the provider loop, and the --harness filter.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestProfileFlagPreselectsStepTwo covers the ratified --profile flag:
// the step is satisfied without asking.
func TestProfileFlagPreselectsStepTwo(t *testing.T) {
	prompt := &scriptedPrompter{
		// three storage references, then the plugin checkbox, then
		// "add a provider?" answered no, telemetry no, daemon yes.
		lines:    []string{"vault://pg", "vault://redis", "vault://s3"},
		confirms: []bool{true, false, false, false, false, true},
	}
	w, _, _, _ := fixture(t, Options{Profile: ProfileServer}, prompt)

	report, err := w.Run(context.Background())
	if err != nil {
		t.Fatalf("Run(--profile server): %v", err)
	}
	if report.State.Profile != ProfileServer {
		t.Errorf("profile = %q, want the flag's value", report.State.Profile)
	}
	for _, q := range prompt.asked {
		if strings.Contains(q, "Which profile") {
			t.Error("--profile was supplied and the wizard still asked")
		}
	}
}

// TestAnUnknownProfileIsRefused keeps a typo from silently becoming a
// local install on a machine meant to be a worker.
func TestAnUnknownProfileIsRefused(t *testing.T) {
	w, _, _, _ := fixture(t, Options{Mode: ModeYes, Profile: "laptop"}, nil)

	_, err := w.Run(context.Background())
	if err == nil {
		t.Fatal("Run accepted a profile that does not exist")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
		t.Errorf("kind = %v (typed %t), want KindInvalidInput", kind, ok)
	}
}

// TestTheWorkerProfileHandsOffAndStops proves two things the spec asks
// for separately: `cascade node enroll` is invoked as a REAL subprocess,
// and the wizard does not then go on to configure a machine its
// controller is about to configure.
func TestTheWorkerProfileHandsOffAndStops(t *testing.T) {
	w, rec, _, _ := fixture(t, Options{Mode: ModeYes, Profile: ProfileWorker}, nil)

	report, err := w.Run(context.Background())
	if err != nil {
		t.Fatalf("Run(--profile worker): %v", err)
	}
	if len(rec.subArgs) != 1 || strings.Join(rec.subArgs[0], " ") != "node enroll" {
		t.Fatalf("subprocess calls = %v, want exactly one `node enroll`", rec.subArgs)
	}
	if report.State.CompletedStep != StepProfile {
		t.Errorf("the run continued to step %v after handing off", report.State.CompletedStep)
	}
	if rec.installed || len(rec.wired) != 0 {
		t.Error("the wizard configured a worker its controller is about to configure")
	}
}

// TestAFailedHandoffFailsTheRun: a worker whose enrollment did not happen
// is not set up, and reporting success would leave an unenrolled machine
// looking ready.
func TestAFailedHandoffFailsTheRun(t *testing.T) {
	w, rec, _, _ := fixture(t, Options{Mode: ModeYes, Profile: ProfileWorker}, nil)
	rec.subErr = errors.New("no controller reachable")

	if _, err := w.Run(context.Background()); err == nil {
		t.Fatal("Run succeeded after `cascade node enroll` failed")
	}
}

// TestServerStorageRefusesALiteralSecret is the leak this step is most
// exposed to: a connection string with a password in it, typed at a
// prompt, would land in config.toml in plaintext.
func TestServerStorageRefusesALiteralSecret(t *testing.T) {
	prompt := &scriptedPrompter{lines: []string{"postgres://user:hunter2@db/cascade"}}
	w, rec, _, _ := fixture(t, Options{Profile: ProfileServer}, prompt)
	rec.secretErr = errors.New("that value looks like a literal secret; use `cascade vault set`")

	_, err := w.Run(context.Background())
	if err == nil {
		t.Fatal("the wizard accepted a literal secret at a storage prompt")
	}
	if !strings.Contains(err.Error(), "vault set") {
		t.Errorf("the refusal does not redirect to the vault: %v", err)
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
		t.Errorf("kind = %v (typed %t), want KindInvalidInput", kind, ok)
	}
}

// TestTheCatalogIsTheLiveRegistry: a checklist offering a plugin this
// build cannot install would install nothing and report success.
func TestTheCatalogIsTheLiveRegistry(t *testing.T) {
	w, rec, out, _ := fixture(t, Options{Mode: ModeYes}, nil)
	rec.entries = []CatalogEntry{
		{Name: "alpha", Description: "one", DefaultOn: true},
		{Name: "beta", Description: "two", DefaultOn: false},
	}

	report, err := w.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := report.State.Plugins; len(got) != 1 || got[0] != "alpha" {
		t.Errorf("selected %v, want only the default-on entry", got)
	}
	for _, name := range []string{"alpha", "beta"} {
		if !strings.Contains(out.String(), name) {
			t.Errorf("the checklist omits %q, so its state cannot be seen:\n%s", name, out)
		}
	}
}

// TestAnEmptyCatalogIsStatedNotInvented: a build with no plugins
// registered is strange but installable, and inventing rows is worse.
func TestAnEmptyCatalogIsStatedNotInvented(t *testing.T) {
	w, rec, out, _ := fixture(t, Options{Mode: ModeYes}, nil)
	rec.entries = nil

	if _, err := w.Run(context.Background()); err != nil {
		t.Fatalf("Run with an empty catalog: %v", err)
	}
	if !strings.Contains(out.String(), "registers no plugins") {
		t.Errorf("an empty catalog rendered as a blank section:\n%s", out)
	}
}

// TestTheHarnessFilterNarrowsAndRefuses covers the ratified --harness
// flag's two halves: it selects a subset of what was DETECTED, and a name
// nothing knows is a refusal rather than a run that wires nothing and
// exits 0.
func TestTheHarnessFilterNarrowsAndRefuses(t *testing.T) {
	t.Run("narrows to the named subset", func(t *testing.T) {
		w, rec, _, _ := fixture(t, Options{Mode: ModeYes, Harnesses: []string{"claude"}}, nil)
		report, err := w.Run(context.Background())
		if err != nil {
			t.Fatalf("Run(--harness claude): %v", err)
		}
		if len(rec.wired) != 1 || rec.wired[0] != "claude" {
			t.Errorf("wired %v, want only the named harness", rec.wired)
		}
		if len(report.State.Harnesses) != 1 {
			t.Errorf("journal records %v", report.State.Harnesses)
		}
	})

	t.Run("cannot wire a harness that is not installed", func(t *testing.T) {
		w, rec, _, _ := fixture(t, Options{Mode: ModeYes, Harnesses: []string{"codex"}}, nil)
		if _, err := w.Run(context.Background()); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if len(rec.wired) != 0 {
			t.Errorf("wired %v; codex is not installed in this fixture", rec.wired)
		}
	})

	t.Run("refuses a harness nothing knows", func(t *testing.T) {
		w, _, _, _ := fixture(t, Options{Mode: ModeYes, Harnesses: []string{"gemini"}}, nil)
		_, err := w.Run(context.Background())
		if err == nil {
			t.Fatal("Run accepted --harness naming a harness this build does not have")
		}
		if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
			t.Errorf("kind = %v (typed %t), want KindInvalidInput", kind, ok)
		}
	})
}

// TestDetectionFailureFailsTheStep: a platform whose harness paths this
// build cannot resolve refuses rather than reporting an empty fleet, and
// the wizard must not turn that refusal into "nothing to wire".
func TestDetectionFailureFailsTheStep(t *testing.T) {
	w, rec, _, _ := fixture(t, Options{Mode: ModeYes}, nil)
	rec.detectErr = cascade.New(cascade.KindUnsupported, "harness detection is not available on this platform")

	_, err := w.Run(context.Background())
	if err == nil {
		t.Fatal("a detection refusal was read as an empty fleet")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindUnsupported {
		t.Errorf("kind = %v (typed %t), want the detector's own kind", kind, ok)
	}
}

// TestTheProviderLoopRunsTheRealSubcommand drives step 5 interactively
// and asserts `cascade provider add` was invoked with the credential mode
// the operator chose.
//
// The assertion is on the ARGV, not on a recorded name: this package has
// no provider code of its own by design, and a wizard that journalled a
// provider without running the subcommand would record one that does not
// work.
func TestTheProviderLoopRunsTheRealSubcommand(t *testing.T) {
	prompt := &scriptedPrompter{
		choices:  []string{ProfileLocal, CredentialKey},
		lines:    []string{"/db", "anthropic"},
		confirms: []bool{true, true, false, false, false, true, true},
	}
	w, rec, _, _ := fixture(t, Options{}, prompt)

	report, err := w.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var argv []string
	for _, call := range rec.subArgs {
		if len(call) > 1 && call[0] == "provider" {
			argv = call
		}
	}
	if argv == nil {
		t.Fatalf("`cascade provider add` was never run; subprocess calls were %v", rec.subArgs)
	}
	if got := strings.Join(argv, " "); got != "provider add anthropic --key" {
		t.Errorf("argv = %q, want the chosen credential mode passed through", got)
	}
	if got := report.State.Providers; len(got) != 1 || got[0] != "anthropic" {
		t.Errorf("journal records providers %v", got)
	}
}

// TestAProviderAddFailureFailsTheStep: a provider the subcommand refused
// is not configured, and journalling it would record one that does not
// work.
func TestAProviderAddFailureFailsTheStep(t *testing.T) {
	prompt := &scriptedPrompter{
		choices:  []string{ProfileLocal, CredentialOAuth},
		lines:    []string{"/db", "anthropic"},
		confirms: []bool{true, true},
	}
	w, rec, _, _ := fixture(t, Options{}, prompt)
	rec.subErr = errors.New("micro-verify rejected the key (401)")

	_, err := w.Run(context.Background())
	if err == nil {
		t.Fatal("Run succeeded after `cascade provider add` failed")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("the subcommand's own message was lost: %v", err)
	}
}

// TestAnUnknownCredentialModeIsRefused keeps a mode this build does not
// map from becoming a malformed flag on a child process, where it would
// surface as a usage error instead of a refusal naming the mode.
func TestAnUnknownCredentialModeIsRefused(t *testing.T) {
	prompt := &scriptedPrompter{
		choices:  []string{ProfileLocal, "passkey"},
		lines:    []string{"/db", "anthropic"},
		confirms: []bool{true, true},
	}
	w, _, _, _ := fixture(t, Options{}, prompt)

	_, err := w.Run(context.Background())
	if err == nil {
		t.Fatal("Run accepted a credential mode with no flag mapping")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
		t.Errorf("kind = %v (typed %t), want KindInvalidInput", kind, ok)
	}
}

// TestTelemetryDefaultsOff is the one question a mode that answers for
// the operator must never answer yes to.
func TestTelemetryDefaultsOff(t *testing.T) {
	for name, mode := range map[string]Mode{"--yes": ModeYes, "--check": ModeCheck} {
		t.Run(name, func(t *testing.T) {
			w, _, _, _ := fixture(t, Options{Mode: mode}, nil)
			report, err := w.Run(context.Background())
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if report.State.Telemetry {
				t.Error("a non-interactive run opted the operator into telemetry")
			}
		})
	}
}
