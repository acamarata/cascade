package secrets

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// fakeCommandModeEnv selects TestMain's re-exec behaviour for
// TestExecRunner: execRunner's own contract is "run an arbitrary external
// command, capture stdout/stderr/exit code" and has nothing to do with a
// shell, so the fixture must not depend on one either — a hardcoded
// /bin/echo or /bin/sh (Windows parity pass 3/4) is not a real executable
// there. Re-exec'ing this same compiled test binary (TestMain intercepts
// before m.Run(), the same idiom cmd/cascade/config/config_edit_test.go
// uses for its fake $EDITOR) is a real, portable child process on every
// platform this package targets.
const fakeCommandModeEnv = "CASCADE_TEST_FAKE_COMMAND_MODE"

// TestMain intercepts before flag parsing when fakeCommandModeEnv is set:
// the process was re-exec'd to stand in for an external command, not to
// run tests. Any other invocation runs m.Run() exactly as if this
// function did not exist.
func TestMain(m *testing.M) {
	switch os.Getenv(fakeCommandModeEnv) {
	case "hello":
		fmt.Println("hello")
		os.Exit(0)
	case "boom":
		fmt.Fprintln(os.Stderr, "boom")
		os.Exit(3)
	}
	os.Exit(m.Run())
}

// isKind reports whether err carries the given taxonomy kind.
func isKind(err error, want cascade.Kind) bool {
	got, ok := cascade.KindOf(err)
	return ok && got == want
}

// failingRunner is a commandRunner that never succeeds, used to force the
// platform backend to report unavailable without touching a real keychain.
func failingRunner(_ context.Context, _ string, _ ...string) ([]byte, error) {
	return nil, &runnerError{err: errors.New("exit status 1"), stderr: "no such tool"}
}

func TestValidateSecretName(t *testing.T) {
	valid := []string{"A", "TOKEN", "api.key", "my-secret_2", strings.Repeat("a", maxSecretNameLen)}
	for _, name := range valid {
		if err := validateSecretName(name); err != nil {
			t.Fatalf("valid name %q refused: %v", name, err)
		}
	}
	invalid := map[string]string{
		"empty":        "",
		"too long":     strings.Repeat("a", maxSecretNameLen+1),
		"leading dash": "-rf",
		"space":        "two words",
		"slash":        "a/b",
		"colon":        "a:b",
		"newline":      "a\nb",
		"nul":          "a\x00b",
		"unicode":      "café",
	}
	for label, name := range invalid {
		err := validateSecretName(name)
		if !isKind(err, cascade.KindInvalidInput) {
			t.Fatalf("%s (%q) was accepted: %v", label, name, err)
		}
	}
}

func TestSortedNames(t *testing.T) {
	in := []string{"c", "a", "b"}
	got := sortedNames(in)
	if strings.Join(got, ",") != "a,b,c" {
		t.Fatalf("sortedNames = %v", got)
	}
	if strings.Join(in, ",") != "c,a,b" {
		t.Fatal("sortedNames mutated its argument")
	}
}

func TestConfigDefaults(t *testing.T) {
	var cfg Config
	if cfg.rand() == nil {
		t.Fatal("the default entropy source is nil")
	}
	if cfg.runner() == nil {
		t.Fatal("the default command runner is nil")
	}
	custom := Config{Runner: failingRunner}
	if _, err := custom.runner()(context.Background(), "x"); err == nil {
		t.Fatal("the injected runner was not used")
	}
}

func TestExecRunner(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}

	t.Setenv(fakeCommandModeEnv, "hello")
	out, err := execRunner(context.Background(), self)
	if err != nil {
		t.Fatalf("echo: %v", err)
	}
	if strings.TrimSpace(string(out)) != "hello" {
		t.Fatalf("echo output = %q", out)
	}

	t.Setenv(fakeCommandModeEnv, "boom")
	_, err = execRunner(context.Background(), self)
	if err == nil {
		t.Fatal("a failing command reported success")
	}
	var re *runnerError
	if !errors.As(err, &re) {
		t.Fatalf("error is not a runnerError: %v", err)
	}
	if !strings.Contains(re.stderr, "boom") {
		t.Fatalf("stderr was not captured: %q", re.stderr)
	}
	if re.Unwrap() == nil {
		t.Fatal("runnerError does not unwrap")
	}
}

func TestErrorKinds(t *testing.T) {
	cases := map[error]cascade.Kind{
		ErrSecretNotFound("A"):              cascade.KindNotFound,
		ErrSecretExists("A"):                cascade.KindConflict,
		ErrCustodyUnavailable("b", nil):     cascade.KindUnavailable,
		ErrCustodyUnavailable("b", errNope): cascade.KindUnavailable,
		ErrNoCustodyAvailable():             cascade.KindUnavailable,
		ErrCustodyCorrupt("b", errNope):     cascade.KindIntegrity,
	}
	for err, want := range cases {
		if !isKind(err, want) {
			t.Fatalf("%v does not carry %v", err, want)
		}
	}
}

var errNope = errors.New("nope")

func TestRunnerErrorMessage(t *testing.T) {
	re := &runnerError{err: errNope, stderr: "diagnostics"}
	if re.Error() != "nope" {
		t.Fatalf("Error() = %q", re.Error())
	}
	if !errors.Is(re, errNope) {
		t.Fatal("runnerError does not wrap its cause")
	}
}
