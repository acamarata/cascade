package secrets

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

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
	out, err := execRunner(context.Background(), "/bin/echo", "hello")
	if err != nil {
		t.Fatalf("echo: %v", err)
	}
	if strings.TrimSpace(string(out)) != "hello" {
		t.Fatalf("echo output = %q", out)
	}
	_, err = execRunner(context.Background(), "/bin/sh", "-c", "echo boom >&2; exit 3")
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
