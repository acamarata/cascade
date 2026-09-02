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
)

// execRoot runs a fresh root command tree with the given args and returns
// combined stdout+stderr. A fresh tree is built per call because cobra
// commands are stateful (parsed flag values persist on the *Command).
func execRoot(t *testing.T, args ...string) (string, error) {
	t.Helper()
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
	for _, want := range []string{version, commit, date, "channel: manual"} {
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

func TestResolvedInstallChannel(t *testing.T) {
	cases := []struct {
		stamped string
		want    string
	}{
		{stamped: "", want: "manual"},
		{stamped: "script", want: "script"},
		{stamped: "brew", want: "brew"},
		{stamped: "oci", want: "oci"},
		{stamped: "node-managed", want: "node-managed"},
		{stamped: "manual", want: "manual"},
		{stamped: "bogus-channel", want: "manual"},
	}

	original := installChannel
	defer func() { installChannel = original }()

	for _, tc := range cases {
		installChannel = tc.stamped
		if got := resolvedInstallChannel(); got != tc.want {
			t.Errorf("resolvedInstallChannel() with stamp %q = %q, want %q", tc.stamped, got, tc.want)
		}
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
