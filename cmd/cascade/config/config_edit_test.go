// Purpose: `cascade config edit` CLI tests — split out of config_test.go
//
//	per R-14.117/Art.10.3 (300-line file cap): the edit verb needs its
//	own fake-$EDITOR harness (fakeEditorScript/newTestRootWithEnv),
//	which is a cohesive, separable seam from the rest of the command
//	tree's tests.
//
// Constraints: fakeEditorScript's $EDITOR stand-in must be a real,
//
//	directly-executable binary on every platform runEditor's
//	exec.CommandContext(editorCommand(deps), tmpPath) runs on — a `#!/bin/sh`
//	script is not one on Windows ("%1 is not a valid Win32 application"),
//	so the stand-in is this same compiled test binary, re-run as a plain
//	child process (TestMain intercepts before m.Run(), the same re-exec
//	idiom providers/sqlite/lock_crossprocess_test.go uses for its helper
//	process) rather than a shell script.
package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/runtime"
)

// fakeEditorLineEnv carries the line TestMain's fake-editor branch
// appends to the file named in its last argument (tmpPath, per
// runEditor's exec.CommandContext(editorCommand(deps), tmpPath) call
// shape — no other argument is ever passed). Its absence means "run the
// real test suite."
const fakeEditorLineEnv = "CASCADE_TEST_FAKE_EDITOR_LINE"

// TestMain intercepts before flag parsing when fakeEditorLineEnv is set:
// the process was re-exec'd (os.Args[0], inherited via exec.Cmd's nil
// Env, per t.Setenv's os.Setenv) to stand in for $EDITOR, not to run
// tests. Any other invocation runs m.Run() exactly as if this function
// did not exist.
func TestMain(m *testing.M) {
	if line, ok := os.LookupEnv(fakeEditorLineEnv); ok {
		os.Exit(runFakeEditor(line))
	}
	os.Exit(m.Run())
}

// runFakeEditor appends line + "\n" to the file named in the process's
// last argument (tmpPath) and returns the process exit code, mirroring
// what the former `printf ... >> "$1"` shell script did, without
// depending on a shell or a shebang existing on the target platform.
func runFakeEditor(line string) int {
	if len(os.Args) < 2 {
		return 1
	}
	path := os.Args[len(os.Args)-1]
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return 1
	}
	_, writeErr := fmt.Fprintf(f, "%s\n", line)
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		return 1
	}
	return 0
}

// fakeEditorScript points $EDITOR at this same test binary and stashes
// appendLine in the real OS environment (t.Setenv, not deps' injected
// Getenv map — runEditor execs a genuine OS subprocess, which inherits
// the process environment, not this test's hermetic Deps) for TestMain
// to pick up when the subprocess starts.
func fakeEditorScript(t *testing.T, appendLine string) string {
	t.Helper()
	t.Setenv(fakeEditorLineEnv, appendLine)
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	return self
}

func TestConfigCLI_Edit_ValidSaveApplies(t *testing.T) {
	dir := t.TempDir()
	editor := fakeEditorScript(t, `[logging]`+"\n"+`level = "debug"`)
	root, _, _ := newTestRootWithEnv(t, dir, map[string]string{"EDITOR": editor})

	if _, _, err := run(t, root, "config", "edit"); err != nil {
		t.Fatalf("edit failed: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `level = "debug"`) {
		t.Fatalf("got:\n%s", data)
	}
}

// TestConfigCLI_Edit_SecretShapedValueRejected is blocking fix 3's
// required test: R-14 CR (P1-E03-W1-S05-T8) found that `edit` applied no
// secret screening at all, so a value `set` would refuse (a bearer-prefix
// token, here) went straight to disk in plaintext when pasted through
// $EDITOR. `edit` must refuse it and leave config.toml untouched, exactly
// like `set` already does for the same literal.
func TestConfigCLI_Edit_SecretShapedValueRejected(t *testing.T) {
	dir := t.TempDir()
	const original = "[logging]\nlevel = \"info\"\n"
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	editor := fakeEditorScript(t, `registry.pubkey_path = "ghp_abcdefghijklmnopqrstuvwxyz0123456789"`)
	root, _, _ := newTestRootWithEnv(t, dir, map[string]string{"EDITOR": editor})

	_, _, err := run(t, root, "config", "edit")
	if err == nil {
		t.Fatal("expected edit to refuse a secret-shaped value")
	}
	if !strings.Contains(err.Error(), "vault set") {
		t.Fatalf("expected vault-set redirect in error, got %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != original {
		t.Fatalf("disk changed on a secret-shaped edit:\n%s", data)
	}
}

func TestConfigCLI_Edit_InvalidSaveNotApplied(t *testing.T) {
	dir := t.TempDir()
	editor := fakeEditorScript(t, "not valid toml {{{")
	root, _, _ := newTestRootWithEnv(t, dir, map[string]string{"EDITOR": editor})

	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("[logging]\nlevel = \"info\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := run(t, root, "config", "edit"); err == nil {
		t.Fatal("expected edit to fail validation")
	}
	data, err := os.ReadFile(filepath.Join(dir, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "[logging]\nlevel = \"info\"\n" {
		t.Fatalf("disk changed on invalid edit:\n%s", data)
	}
}

// newTestRootWithEnv is newTestRoot plus an injected extra-env map (for
// EDITOR), since Deps.Getenv only special-cases CASCADE_HOME by default.
func newTestRootWithEnv(t *testing.T, homeDir string, env map[string]string) (*cobra.Command, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	root := &cobra.Command{Use: "cascade"}
	root.PersistentFlags().Bool("json", false, "")
	root.PersistentFlags().String("profile", "", "")
	root.PersistentFlags().String("config", "", "")
	root.PersistentFlags().BoolP("quiet", "q", false, "")
	root.PersistentFlags().BoolP("verbose", "v", false, "")
	root.PersistentFlags().Bool("no-color", false, "")

	getenv := func(k string) string {
		if k == "CASCADE_HOME" {
			return homeDir
		}
		if v, ok := env[k]; ok {
			return v
		}
		return ""
	}
	paths, err := runtime.NewPathProvider(getenv, func() (string, error) { return homeDir, nil })
	if err != nil {
		t.Fatal(err)
	}
	deps := Deps{
		Paths:   paths,
		Getenv:  getenv,
		Clock:   runtime.NewFixedClock(time.Unix(0, 0)),
		Environ: func() []string { return nil },
	}
	root.AddCommand(NewConfigCmd(deps))

	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	return root, &out, &errOut
}
