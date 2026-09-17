package main

// Purpose: `cascade init --config` and the CASCADE_INIT_* surface driven
//   through the REAL root command (P1-E16-W4-S35-T7).
// Constraints: Art.7.1 — HOME, CASCADE_HOME and the working directory are
//   temp trees, and daemon.install=false keeps the suite from installing
//   a service on the machine running it. These are the executable form of
//   the scenarios documented in testdata/scripts/init_*.txtar (this
//   module carries no testscript runner; see those files' own headers).

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/runtime/initconfig"
	"github.com/acamarata/cascade/pkg/cascade"
)

// initHome pins HOME, CASCADE_HOME and the working directory, clears the
// three CASCADE_INIT_* variables the suite's own environment might carry,
// and returns the cascade home.
func initHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	cascadeHome := filepath.Join(home, ".cascade")
	t.Setenv("HOME", home)
	t.Setenv("CASCADE_HOME", cascadeHome)
	t.Setenv(initconfig.EnvNoInput, "")
	t.Setenv(initconfig.EnvConfig, "")
	t.Setenv(initconfig.EnvYes, "")
	t.Chdir(t.TempDir())
	return cascadeHome
}

// writeSetupFile plants a setup file and returns its path.
func writeSetupFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cascade-init.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing the setup file: %v", err)
	}
	return path
}

// completeSetup answers every site, so no guard can fire.
const completeSetup = `
schema = "cascade.init/v1"
profile = "local"

[plugins]
enable = ["cascade-claude"]

[harnesses]
detect = true
install = []

[telemetry]
enabled = false

[daemon]
install = false
`

// TestInitConfigValid is the ticket's first acceptance scenario: a valid
// setup file processes every field and exits 0, with no prompts.
func TestInitConfigValid(t *testing.T) {
	cascadeHome := initHome(t)
	path := writeSetupFile(t, completeSetup)

	out, err := execRoot(t, "init", "--config", path)
	if err != nil {
		t.Fatalf("init --config: %v\n%s", err, out)
	}
	if !strings.Contains(out, "profile     local") {
		t.Errorf("the run did not take the file's profile:\n%s", out)
	}
	if !strings.Contains(out, "cascade-claude") {
		t.Errorf("the run did not enable the file's plugin:\n%s", out)
	}
	if !strings.Contains(out, "not installed") {
		t.Errorf("the daemon was installed against a file that said false:\n%s", out)
	}
	if _, statErr := os.Stat(cascadeHome); statErr != nil {
		t.Errorf("a file-driven run did not create the cascade home: %v", statErr)
	}
}

// TestInitConfigFromTheEnvironment: CASCADE_INIT_CONFIG is the same
// instruction as --config, and a run honouring only the flag would hang
// in any CI that sets the variable.
func TestInitConfigFromTheEnvironment(t *testing.T) {
	initHome(t)
	t.Setenv(initconfig.EnvConfig, writeSetupFile(t, completeSetup))

	if out, err := execRoot(t, "init"); err != nil {
		t.Fatalf("init with CASCADE_INIT_CONFIG: %v\n%s", err, out)
	}
}

// TestInitConfigLiteralSecret is the second scenario: a key written into
// the file is refused, and no credential is recorded.
func TestInitConfigLiteralSecret(t *testing.T) {
	cascadeHome := initHome(t)
	path := writeSetupFile(t, `
schema = "cascade.init/v1"
[[providers]]
name = "anthropic"
auth = "key-env"
key_env = "sk-ant-api03-`+strings.Repeat("A", 88)+`"
`)

	_, err := execRoot(t, "init", "--config", path)
	if err == nil {
		t.Fatal("init accepted a setup file with a key written into it")
	}
	if !strings.Contains(err.Error(), "vault set") {
		t.Errorf("the refusal does not redirect to the vault: %v", err)
	}
	if strings.Contains(err.Error(), "sk-ant-api03") {
		t.Errorf("the refusal quotes the credential: %v", err)
	}
	// Refused BEFORE any step executed, so nothing was configured.
	if _, statErr := os.Stat(cascadeHome); !os.IsNotExist(statErr) {
		t.Errorf("a refused setup file still created %s", cascadeHome)
	}
}

// TestInitConfigOAuth is the third scenario (R-14.53): an oauth directive
// exits non-zero before any step runs, citing the alternative.
func TestInitConfigOAuth(t *testing.T) {
	cascadeHome := initHome(t)
	path := writeSetupFile(t, `
schema = "cascade.init/v1"
[[providers]]
name = "anthropic"
auth = "oauth"
key_env = "ANTHROPIC_API_KEY"
`)

	_, err := execRoot(t, "init", "--config", path)
	if err == nil {
		t.Fatal("init attempted a browser flow in a file-driven run")
	}
	if !strings.Contains(err.Error(), "key-env") {
		t.Errorf("the refusal does not cite the key-auth alternative: %v", err)
	}
	if _, statErr := os.Stat(cascadeHome); !os.IsNotExist(statErr) {
		t.Errorf("a refused setup file still created %s", cascadeHome)
	}
}

// TestInitNoInput is the fourth scenario: CASCADE_NO_INPUT with nothing
// to answer from refuses at the first site that would block, and nothing
// partial is left behind.
func TestInitNoInput(t *testing.T) {
	cascadeHome := initHome(t)
	t.Setenv(initconfig.EnvNoInput, "1")

	_, err := execRoot(t, "init")
	if err == nil {
		t.Fatal("a NO_INPUT run answered a question nobody supplied a value for")
	}
	if !strings.Contains(err.Error(), "--yes") || !strings.Contains(err.Error(), "--config") {
		t.Errorf("the refusal does not name both ways out: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(cascadeHome, "init-state.json")); !os.IsNotExist(statErr) {
		t.Error("the refusal left a resumable journal behind")
	}
}

// TestInitNoInputIsSatisfiedByYesOrByAFile is the half proving the guard
// is not simply refusing everything.
func TestInitNoInputIsSatisfiedByYesOrByAFile(t *testing.T) {
	t.Run("--yes", func(t *testing.T) {
		initHome(t)
		t.Setenv(initconfig.EnvNoInput, "1")
		if out, err := execRoot(t, "init", "--yes", "--no-daemon"); err != nil {
			t.Fatalf("NO_INPUT with --yes: %v\n%s", err, out)
		}
	})

	t.Run("a complete setup file", func(t *testing.T) {
		initHome(t)
		t.Setenv(initconfig.EnvNoInput, "1")
		path := writeSetupFile(t, completeSetup)
		if out, err := execRoot(t, "init", "--config", path); err != nil {
			t.Fatalf("NO_INPUT with a complete file: %v\n%s", err, out)
		}
	})
}

// TestInitConfigUnknownKey: a typo in a setup file is refused with a
// suggestion, before any step executes.
func TestInitConfigUnknownKey(t *testing.T) {
	initHome(t)
	path := writeSetupFile(t, "schema = \"cascade.init/v1\"\nprofil = \"local\"\n")

	_, err := execRoot(t, "init", "--config", path)
	if err == nil {
		t.Fatal("init accepted a setup file with an unknown key")
	}
	if !strings.Contains(err.Error(), `did you mean "profile"`) {
		t.Errorf("the refusal carries no suggestion: %v", err)
	}
}

// TestInitConfigFutureSchema refuses a file written for a newer build
// rather than acting on whichever fields this one happens to recognise.
func TestInitConfigFutureSchema(t *testing.T) {
	initHome(t)
	path := writeSetupFile(t, `schema = "cascade.init/v99"`)

	_, err := execRoot(t, "init", "--config", path)
	if err == nil {
		t.Fatal("init read a setup file from a newer schema")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindUnsupported {
		t.Errorf("kind = %v (typed %t), want KindUnsupported", kind, ok)
	}
}

// TestReconvergeNeedsAConfig: without a setup file there is no desired
// state, and comparing the machine to the shipped defaults would try to
// revert every deliberate choice on it.
func TestReconvergeNeedsAConfig(t *testing.T) {
	initHome(t)

	_, err := execRoot(t, "init", "--reconverge", "--yes")
	if err == nil {
		t.Fatal("a reconverge ran with nothing to converge toward")
	}
	if !strings.Contains(err.Error(), "--config") {
		t.Errorf("the refusal does not say what is missing: %v", err)
	}
}

// TestInitReconverge drives the convergence contract end to end: set the
// machine up from a file, then converge against the same file and require
// no work.
//
// --check for the second run, because the apply path hands off to
// `cascade config set` as a child process and os.Executable under
// `go test` is the TEST binary — see ErrSubprocessInTest. What this
// asserts is the DECISION: converging against the same file finds nothing
// to do. The apply is covered where the subprocess is a seam, in
// internal/runtime/init's own reconverge tests.
func TestInitReconverge(t *testing.T) {
	initHome(t)
	path := writeSetupFile(t, completeSetup)

	if out, err := execRoot(t, "init", "--config", path); err != nil {
		t.Fatalf("first run: %v\n%s", err, out)
	}
	out, err := execRoot(t, "init", "--config", path, "--reconverge", "--check")
	if err != nil {
		t.Fatalf("converging against the same file reported work to do: %v\n%s", err, out)
	}
	if !strings.Contains(out, "already converged") {
		t.Errorf("a second run against the same file did not report itself converged:\n%s", out)
	}
}

// TestASubprocessHandOffIsRefusedUnderTest pins the guard that made the
// test above hang before it existed: a hand-off from a test that reaches
// the real applier re-execs the TEST binary, running the whole suite as a
// child, which runs it again.
func TestASubprocessHandOffIsRefusedUnderTest(t *testing.T) {
	sub := initSubprocess{in: strings.NewReader(""), out: os.Stdout, err: os.Stderr}

	if err := sub.Run(context.Background(), "config", "set", "a.b", "c"); err == nil {
		t.Fatal("a test re-exec'd the test binary as a cascade subcommand")
	} else if !errors.Is(err, ErrSubprocessInTest) {
		t.Errorf("err = %v, want ErrSubprocessInTest", err)
	}
	if _, err := runCascade(context.Background(), "elevate-helper", "--enroll"); !errors.Is(err, ErrSubprocessInTest) {
		t.Errorf("runCascade err = %v, want ErrSubprocessInTest", err)
	}
	// The adapter's own test drives a REAL child with the test binary's
	// own flags, and that must still work — otherwise the guard would
	// have removed the only coverage this adapter has.
	if err := sub.Run(context.Background(), "-test.run=^$", "-test.count=1"); err != nil {
		t.Errorf("the guard blocked the adapter's own real-child test: %v", err)
	}
}
