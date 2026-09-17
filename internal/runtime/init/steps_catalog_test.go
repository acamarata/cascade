package init

// Purpose: steps 4-7's contracts (P1-E16-W4-S35-T6): the plugin catalog,
//   the provider loop, the --harness filter and the telemetry default.
//   Split from steps_test.go, which keeps steps 2-3, for Art.10.3's
//   300-line cap.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

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

// TestATier2DetectionRefusalSkipsTheStep is the windows case, found by
// the windows CI lane and not by any local run: the detector refuses on a
// platform whose harness paths this build does not resolve, and step 6
// returning that error killed the whole setup there.
//
// A step that does not apply on a platform is the same situation step 8
// is in with the service manager: skipped, with a stated reason, and a
// nil error. A headless one-shot is exactly what tier-2 promises.
func TestATier2DetectionRefusalSkipsTheStep(t *testing.T) {
	w, rec, out, _ := fixture(t, Options{Mode: ModeYes}, nil)
	rec.detectErr = cascade.New(cascade.KindUnsupported,
		"context: harness detection is not available on this platform (tier-2)")

	report, err := w.Run(context.Background())
	if err != nil {
		t.Fatalf("a tier-2 platform failed the whole setup: %v", err)
	}
	if !report.State.Complete() {
		t.Errorf("the run stopped at step %v", report.State.CompletedStep)
	}
	if len(rec.wired) != 0 {
		t.Errorf("wired %v after detection refused", rec.wired)
	}
	if report.State.HarnessSkipReason == "" {
		t.Error("step 6 wired nothing and recorded no reason, so the summary has nothing to say")
	}
	if !strings.Contains(out.String(), "tier-2") {
		t.Errorf("the skip does not name the tier:\n%s", out)
	}
	// "none" and "this platform cannot look" are different answers.
	if !strings.Contains(out.String(), "harnesses   none (") {
		t.Errorf("the summary reports a bare none for a platform nobody could check:\n%s", out)
	}
}

// TestANonPlatformDetectionFailureStillFails is the other half: only
// "this step does not apply here" is a skip. An unreadable home or a
// cancelled context is a real failure, and the branch above must not
// swallow it.
func TestANonPlatformDetectionFailureStillFails(t *testing.T) {
	w, rec, _, _ := fixture(t, Options{Mode: ModeYes}, nil)
	rec.detectErr = cascade.New(cascade.KindUnavailable, "HOME is unset")

	_, err := w.Run(context.Background())
	if err == nil {
		t.Fatal("a real detection failure was swallowed as a platform skip")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindUnavailable {
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
