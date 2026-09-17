package init

// Purpose: the setup file's effect on the two steps that need more than
//   an answer (P1-E16-W4-S35-T7): the provider DIRECTIVES, and
//   harnesses.detect = false.
// Constraints: driven through the real steps, like the rest of the spec
//   suite -- these two read Options.Spec directly rather than through
//   the prompter, and that is exactly what must not rot silently.

import (
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/runtime/initconfig"
)

// TestProviderDirectivesDriveTheProviderStep: the file names a variable,
// the step reads it at the moment the provider is added, and the argv
// carries the variable NAME and never its value.
func TestProviderDirectivesDriveTheProviderStep(t *testing.T) {
	spec := specFor(t, initconfig.Flags{}, nil, `
schema = "cascade.init/v1"
[[providers]]
name = "anthropic"
auth = "key-env"
key_env = "SOME_KEY"
verify = true
base_url = "https://example.invalid"
`)
	w, rec, _, _ := fixture(t, Options{Spec: spec}, NewSpecPrompter(spec))
	rec.env["SOME_KEY"] = "a-value"

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
		t.Fatalf("`cascade provider add` was never run: %v", rec.subArgs)
	}
	joined := strings.Join(argv, " ")
	if !strings.Contains(joined, "--key-env SOME_KEY") {
		t.Errorf("argv = %q, want the variable NAME passed through", joined)
	}
	if strings.Contains(joined, "a-value") {
		t.Errorf("argv carries the key's VALUE: %q", joined)
	}
	if !strings.Contains(joined, "--base-url https://example.invalid") {
		t.Errorf("argv = %q, want the file's base_url", joined)
	}
	if strings.Contains(joined, "--no-verify") {
		t.Errorf("argv = %q, but the file asked for verification", joined)
	}
	if got := report.State.Providers; len(got) != 1 || got[0] != "anthropic" {
		t.Errorf("journal records %v", got)
	}
}

// TestAnUnsetKeyVariableRefusesBeforeTheProviderIsAdded: a provider added
// with an empty key fails later, somewhere that cannot say which setup
// file sent it.
func TestAnUnsetKeyVariableRefusesBeforeTheProviderIsAdded(t *testing.T) {
	spec := specFor(t, initconfig.Flags{}, nil, `
schema = "cascade.init/v1"
[[providers]]
name = "anthropic"
auth = "key-env"
key_env = "NEVER_SET"
`)
	w, rec, _, _ := fixture(t, Options{Spec: spec}, NewSpecPrompter(spec))

	_, err := w.Run(context.Background())
	if err == nil {
		t.Fatal("the run added a provider whose key variable is unset")
	}
	if !strings.Contains(err.Error(), "NEVER_SET") {
		t.Errorf("the refusal does not name the variable: %v", err)
	}
	for _, call := range rec.subArgs {
		if len(call) > 0 && call[0] == "provider" {
			t.Errorf("`cascade provider add` ran anyway: %v", call)
		}
	}
}

// TestDetectionOffLeavesEveryHarnessAlone: a file that turned detection
// off is asking for no harness to be touched, and detecting anyway would
// read the operator's machine after they said not to.
func TestDetectionOffLeavesEveryHarnessAlone(t *testing.T) {
	spec := specFor(t, initconfig.Flags{}, nil, `
schema = "cascade.init/v1"
[harnesses]
detect = false
install = ["claude"]
`)
	w, rec, out, _ := fixture(t, Options{Spec: spec}, NewSpecPrompter(spec))

	report, err := w.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(rec.wired) != 0 {
		t.Errorf("wired %v against a file that turned detection off", rec.wired)
	}
	if !strings.Contains(out.String(), "turned harness detection off") {
		t.Errorf("the skip was silent:\n%s", out)
	}
	if report.State.HarnessSkipReason == "" {
		t.Error("the journal records no reason, so the summary has nothing to say")
	}
}

// TestSpecProfileNamesMatchTheWizards is the seam assertion for the one
// duplicated constant set: initconfig validates a profile name without
// importing this package, so the two lists must agree.
func TestSpecProfileNamesMatchTheWizards(t *testing.T) {
	for _, name := range profiles() {
		if _, err := initconfig.Resolve(initconfig.Flags{Profile: name}, nil, nil); err != nil {
			t.Errorf("initconfig refuses %q, which this package offers: %v", name, err)
		}
	}
	if _, err := initconfig.Resolve(initconfig.Flags{Profile: "laptop"}, nil, nil); err == nil {
		t.Error("initconfig accepts a profile this package does not offer")
	}
}

// TestADoomedInvocationLeavesNoJournal: a prompt-guard refusal is
// deterministic given the command line, so the journal it left would make
// the next — correctly invoked — run report a resume and skip steps the
// refused run only appeared to complete.
func TestADoomedInvocationLeavesNoJournal(t *testing.T) {
	spec := specFor(t, initconfig.Flags{}, map[string]string{initconfig.EnvNoInput: "1"}, "")
	w, _, _, home := fixture(t, Options{Spec: spec}, NewSpecPrompter(spec))

	if _, err := w.Run(context.Background()); err == nil {
		t.Fatal("the guard did not fire")
	}
	if _, _, err := LoadState(home); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if _, found, _ := LoadState(home); found {
		t.Error("a refusal that can never succeed left a resumable journal behind")
	}
}
