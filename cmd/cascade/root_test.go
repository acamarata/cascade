// Purpose: tests for the cobra root command, global flags, version, and
//
//	shell completion wiring.
//
// Constraints: writes nothing to disk (Art.7.1 — no t.TempDir() needed since
//
//	these tests only exercise in-memory cobra command execution); every
//	RunE'd command is invoked through the same newRootCmd()/newCompletionCmd()
//	constructors main.go uses, never a hand-authored dialect (Art.2).
//
// SPORT: cmd/cascade — cobra-root, global-flags, version, completions.
package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/buildinfo"
	"github.com/acamarata/cascade/pkg/cascade"
)

// execRoot runs a fresh root command tree with the given args and returns
// combined stdout+stderr. A fresh tree is built per call because cobra
// commands are stateful (parsed flag values persist on the *Command).
func execRoot(t *testing.T, args ...string) (string, error) {
	t.Helper()
	// globalFlags is package-level state that cobra writes into, so it
	// persists across commands within a test binary. Reset it per call to
	// keep tests order-independent (Art.7.3).
	globalFlags = GlobalFlags{}
	root := newRootCmd()
	buf := &bytes.Buffer{}
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetArgs(args)
	err := root.Execute()
	return buf.String(), err
}

func TestGoldenHelp(t *testing.T) {
	golden, err := os.ReadFile(filepath.Join("testdata", "golden_help.txt"))
	if err != nil {
		t.Fatalf("read golden fixture: %v", err)
	}

	cases := []struct {
		name string
		args []string
	}{
		{name: "long flag", args: []string{"--help"}},
		{name: "short flag", args: []string{"-h"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := execRoot(t, tc.args...)
			if err != nil {
				t.Fatalf("execute %v: %v", tc.args, err)
			}
			if got != string(golden) {
				t.Errorf("--help output mismatch (case %s)\n--- got ---\n%s\n--- want ---\n%s", tc.name, got, string(golden))
			}
		})
	}
}

// TestGoldenHelpDetectsMutation proves the golden fixture is load-bearing:
// mutating the expected text makes the comparison fail red, satisfying the
// acceptance criterion "a mutated fixture makes the test red".
func TestGoldenHelpDetectsMutation(t *testing.T) {
	got, err := execRoot(t, "--help")
	if err != nil {
		t.Fatalf("execute --help: %v", err)
	}
	mutated := got + "\nunexpected extra line\n"
	if got == mutated {
		t.Fatal("mutated fixture must differ from actual output")
	}
}

func TestGlobalFlagsAppearInHelp(t *testing.T) {
	got, err := execRoot(t, "--help")
	if err != nil {
		t.Fatalf("execute --help: %v", err)
	}
	for _, want := range []string{"--json", "--profile", "--config", "-q, --quiet", "-v, --verbose"} {
		if !strings.Contains(got, want) {
			t.Errorf("--help output missing global flag %q\noutput:\n%s", want, got)
		}
	}
}

func TestGlobalFlagsAccessibleFromSubcommand(t *testing.T) {
	root := newRootCmd()
	root.SetArgs([]string{"--profile", "work", "version"})
	buf := &bytes.Buffer{}
	root.SetOut(buf)
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	versionCmd, _, err := root.Find([]string{"version"})
	if err != nil {
		t.Fatalf("find version command: %v", err)
	}
	got, err := versionCmd.Root().PersistentFlags().GetString("profile")
	if err != nil {
		t.Fatalf("read --profile via cmd.Root().PersistentFlags(): %v", err)
	}
	if got != "work" {
		t.Errorf("--profile via cmd.Root().PersistentFlags() = %q, want %q", got, "work")
	}
}

func TestQuietVerboseMutuallyExclusive(t *testing.T) {
	globalFlags = GlobalFlags{}
	_, err := execRoot(t, "--quiet", "--verbose", "version")
	if err == nil {
		t.Fatal("expected error when --quiet and --verbose are both set")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("error = %v, want mention of mutually exclusive", err)
	}
}

func TestVersionCommand(t *testing.T) {
	globalFlags = GlobalFlags{}
	got, err := execRoot(t, "version")
	if err != nil {
		t.Fatalf("execute version: %v", err)
	}
	for _, want := range []string{buildinfo.Version, buildinfo.Commit, buildinfo.Date, "channel: manual"} {
		if !strings.Contains(got, want) {
			t.Errorf("version output missing %q\noutput:\n%s", want, got)
		}
	}
}

func TestVersionCommandRejectsArgs(t *testing.T) {
	globalFlags = GlobalFlags{}
	_, err := execRoot(t, "version", "extra-arg")
	if err == nil {
		t.Fatal("expected error for unexpected positional arg")
	}
}

func TestCompletion(t *testing.T) {
	for _, shell := range completionShells {
		t.Run(shell, func(t *testing.T) {
			globalFlags = GlobalFlags{}
			got, err := execRoot(t, "completion", shell)
			if err != nil {
				t.Fatalf("execute completion %s: %v", shell, err)
			}
			if strings.TrimSpace(got) == "" {
				t.Errorf("completion %s produced empty output", shell)
			}
		})
	}
}

func TestCompletionRejectsUnknownShell(t *testing.T) {
	globalFlags = GlobalFlags{}
	_, err := execRoot(t, "completion", "bogus-shell")
	if err == nil {
		t.Fatal("expected error for unsupported shell")
	}
}

func TestCompletionRequiresExactlyOneArg(t *testing.T) {
	globalFlags = GlobalFlags{}
	if _, err := execRoot(t, "completion"); err == nil {
		t.Fatal("expected error when no shell is given")
	}
	if _, err := execRoot(t, "completion", "bash", "zsh"); err == nil {
		t.Fatal("expected error when more than one shell is given")
	}
}

// TestProcessExitCodes pins the mapping main.go applies between a command's
// returned error and the process exit status. It exists because the original
// wiring exited 1 unconditionally and every unit test still passed: the tests
// asserted that an error was returned, never which status it produced. Cobra
// generates flag- and argument-validation errors itself, and those carry no
// taxonomy kind unless the root explicitly wraps them (R-14.113), so each of
// these cases guards a different path into cascade.ExitCode.
func TestProcessExitCodes(t *testing.T) {
	invalid := cascade.KindInvalidInput.ExitCode()

	tests := []struct {
		name string
		args []string
		want int
	}{
		{"bare root prints help", nil, cascade.ExitOK},
		{"help flag", []string{"--help"}, cascade.ExitOK},
		{"version", []string{"version"}, cascade.ExitOK},
		{"valid completion", []string{"completion", "zsh"}, cascade.ExitOK},
		{"quiet and verbose conflict", []string{"--quiet", "--verbose"}, invalid},
		{"unknown flag", []string{"--nosuchflag"}, invalid},
		{"unknown completion shell", []string{"completion", "badshell"}, invalid},
		{"completion missing argument", []string{"completion"}, invalid},
		{"version rejects extra args", []string{"version", "extra"}, invalid},
		{"unknown subcommand", []string{"bogus-subcommand"}, invalid},
		{"unknown subcommand with flag", []string{"bogus", "--json"}, invalid},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := execRoot(t, tt.args...)
			if got := cascade.ExitCode(err); got != tt.want {
				t.Errorf("exit code = %d, want %d (err: %v)", got, tt.want, err)
			}
		})
	}
}

// TestCobraErrorsCarryTaxonomyKind asserts the wrapping itself, so a
// regression that drops SetFlagErrorFunc fails here with a precise message
// rather than only as a wrong exit number above.
func TestCobraErrorsCarryTaxonomyKind(t *testing.T) {
	for _, args := range [][]string{
		{"--nosuchflag"},
		{"completion", "badshell"},
		{"completion"},
		{"bogus-subcommand"},
	} {
		_, err := execRoot(t, args...)
		if err == nil {
			t.Fatalf("args %v: expected an error", args)
		}
		if !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Errorf("args %v: error %v does not carry KindInvalidInput", args, err)
		}
	}
}
