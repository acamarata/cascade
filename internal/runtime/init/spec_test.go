package init

// Purpose: the non-interactive spec's effect on the wizard
//   (P1-E16-W4-S35-T7): what a setup file answers, and what
//   CASCADE_NO_INPUT refuses.
// Constraints: the prompter matches on the question's TEXT, so every
//   branch is asserted by driving the REAL step. A step whose wording
//   changes without spec.go changing fails here.

import (
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/runtime/initconfig"
	"github.com/acamarata/cascade/pkg/cascade"
)

// specFor resolves a spec from a file body, failing the test on a parse
// or merge error.
func specFor(t *testing.T, flags initconfig.Flags, env map[string]string, body string) *initconfig.Spec {
	t.Helper()
	var file *initconfig.InitConfig
	if body != "" {
		parsed, err := initconfig.Parse([]byte(body))
		if err != nil {
			t.Fatalf("parsing the fixture setup file: %v", err)
		}
		file = parsed
	}
	spec, err := initconfig.Resolve(flags, func(k string) string { return env[k] }, file)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	return spec
}

// TestPromptGuard is the whole contract of CASCADE_NO_INPUT, in its three
// cases. The two passing cases matter as much as the refusing one: a
// guard that refused everything would satisfy the first alone.
func TestPromptGuard(t *testing.T) {
	noInput := map[string]string{initconfig.EnvNoInput: "1"}

	t.Run("no yes and no spec: the first prompt refuses", func(t *testing.T) {
		spec := specFor(t, initconfig.Flags{}, noInput, "")
		w, _, _, _ := fixture(t, Options{Spec: spec}, NewSpecPrompter(spec))

		_, err := w.Run(context.Background())
		if err == nil {
			t.Fatal("a NO_INPUT run answered a question nobody supplied a value for")
		}
		if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
			t.Errorf("kind = %v (typed %t), want KindInvalidInput", kind, ok)
		}
		for _, want := range []string{"--yes", "--config", "Which profile"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the refusal omits %q: %v", want, err)
			}
		}
	})

	t.Run("--yes passes every site", func(t *testing.T) {
		spec := specFor(t, initconfig.Flags{Yes: true}, noInput, "")
		w, rec, _, _ := fixture(t, Options{Spec: spec}, NewSpecPrompter(spec))

		report, err := w.Run(context.Background())
		if err != nil {
			t.Fatalf("NO_INPUT with --yes: %v", err)
		}
		if !report.State.Complete() {
			t.Errorf("the run stopped at step %v", report.State.CompletedStep)
		}
		if len(rec.wired) == 0 {
			t.Error("--yes wired no harness, so the run did nothing worth guarding")
		}
	})

	t.Run("a complete setup file passes every site", func(t *testing.T) {
		assertCompleteSpecPassesEverySite(t, noInput)
	})
}

// completeSetupFile answers every site the wizard can ask about, so no
// guard can fire against it.
const completeSetupFile = `
schema = "cascade.init/v1"
profile = "local"
[plugins]
enable = ["claude"]
[harnesses]
detect = true
install = ["claude"]
[telemetry]
enabled = false
[daemon]
install = false
`

// assertCompleteSpecPassesEverySite is the half that proves the guard is
// not simply refusing everything.
func assertCompleteSpecPassesEverySite(t *testing.T, env map[string]string) {
	t.Helper()
	spec := specFor(t, initconfig.Flags{}, env, completeSetupFile)
	w, rec, _, _ := fixture(t, Options{Spec: spec}, NewSpecPrompter(spec))

	report, err := w.Run(context.Background())
	if err != nil {
		t.Fatalf("NO_INPUT with a complete spec: %v", err)
	}
	if !report.State.Complete() {
		t.Errorf("the run stopped at step %v", report.State.CompletedStep)
	}
	if len(rec.wired) != 1 || rec.wired[0] != "claude" {
		t.Errorf("wired %v, want the harness the file named", rec.wired)
	}
	if rec.installed {
		t.Error("the daemon was installed against a file that said install = false")
	}
}

// TestTheSpecAnswersEveryQuestionTheStepsAsk is the anti-drift assertion:
// the prompter matches on question text, so every branch is driven
// through the real steps rather than by calling the prompter with a
// made-up string.
func TestTheSpecAnswersEveryQuestionTheStepsAsk(t *testing.T) {
	spec := specFor(t, initconfig.Flags{}, nil, `
schema = "cascade.init/v1"
profile = "server"
[plugins]
enable = ["claude"]
[harnesses]
detect = true
install = ["opencode"]
[server]
postgres_dsn_env = "PG"
redis_url_env = "REDIS"
s3_env_prefix = "S3"
[telemetry]
enabled = true
[daemon]
install = true
`)
	w, rec, out, _ := fixture(t, Options{Spec: spec}, NewSpecPrompter(spec))
	// Two entries, one of them NOT named by the file, so "the file
	// selected a subset" and "the file selected nothing" are
	// distinguishable. With the fixture's single entry they were not,
	// and this test asserted a selection that never happened.
	rec.entries = []CatalogEntry{
		{Name: "claude", Description: "the first", DefaultOn: true},
		{Name: "alpha", Description: "the second", DefaultOn: false},
	}

	report, err := w.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.State.Profile != "server" {
		t.Errorf("profile = %q, want the file's", report.State.Profile)
	}
	// The file's [plugins] key selects NOTHING on a fresh run: every
	// catalog entry is a builtin and is active either way (R-14.277).
	// Journalling the file's subset would record a machine state that is
	// not this machine's.
	if got := report.State.Plugins; len(got) != 2 || got[0] != "alpha" || got[1] != "claude" {
		t.Errorf("plugins = %v, want every builtin this binary ships", got)
	}
	if !strings.Contains(out.String(), "nothing to select") {
		t.Errorf("the file named plugins and was not told the naming selects nothing:\n%s", out)
	}
	if got := rec.wired; len(got) != 1 || got[0] != "opencode" {
		t.Errorf("wired %v, want only the harness the file named", got)
	}
	if !report.State.Telemetry {
		t.Error("telemetry off against a file that turned it on")
	}
	if !rec.installed {
		t.Error("the daemon was not installed against a file that asked for it")
	}
	for _, want := range []string{"env:PG", "env:REDIS", "env:S3"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the storage step did not use the file's %q:\n%s", want, out)
		}
	}
}
