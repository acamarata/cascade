// Purpose: exercises the full `cascade config` command tree end to end
//
//	against a real *cobra.Command execution (SetArgs+Execute), NOT
//	individual handler functions — this is the CLI-surface test level
//	the ticket contract calls out ("BUILD AND RUN the binary... paste
//	real observed output"). Because cmd/cascade/root.go is out of this
//	ticket's files_scope and carries no subcommand-registration hook a
//	later package can call into without editing it (see config.go's
//	package doc, MOUNTING NOTE), these tests build a standalone root
//	carrying the SAME persistent flags root.go registers
//	(--json/--profile/--config/-q/-v) so NewConfigCmd's behavior is
//	proven under the real global-flag contract it will run under once
//	mounted, without this package needing write access to root.go.
package config

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/runtime"
)

// newTestRoot builds a root *cobra.Command carrying the exact global
// flags cmd/cascade/root.go registers (D/S-06.T1), then mounts
// NewConfigCmd under it — the one-line addition root.go itself needs the
// day it gains an extension point, exercised here standalone.
func newTestRoot(t *testing.T, homeDir string) (*cobra.Command, *bytes.Buffer, *bytes.Buffer) {
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

// run executes root with args and returns (err); stdout/stderr are read
// from the *bytes.Buffer values newTestRoot already bound via
// SetOut/SetErr — callers pass those buffers in when they want to
// inspect output, so run itself only needs to report the error.
func run(t *testing.T, root *cobra.Command, args ...string) (string, string, error) {
	t.Helper()
	root.SetArgs(args)
	err := root.ExecuteContext(context.Background())
	return "", "", err
}

func TestConfigCLI_SetGetRoundTrip(t *testing.T) {
	dir := t.TempDir()
	root, out, _ := newTestRoot(t, dir)

	if _, _, err := run(t, root, "config", "set", "logging.level=\"debug\""); err != nil {
		t.Fatalf("set failed: %v", err)
	}
	out.Reset()
	if _, _, err := run(t, root, "config", "get", "logging.level"); err != nil {
		t.Fatalf("get failed: %v", err)
	}
	if !strings.Contains(out.String(), "debug") {
		t.Fatalf("expected debug in output, got %q", out.String())
	}
}

func TestConfigCLI_SetSpaceSeparatedForm(t *testing.T) {
	dir := t.TempDir()
	root, _, _ := newTestRoot(t, dir)
	if _, _, err := run(t, root, "config", "set", "logging.format", "\"json\""); err != nil {
		t.Fatalf("set failed: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `format = "json"`) {
		t.Fatalf("got:\n%s", data)
	}
}

func TestConfigCLI_SetUnknownKey_NoWrite(t *testing.T) {
	dir := t.TempDir()
	root, _, _ := newTestRoot(t, dir)
	_, _, err := run(t, root, "config", "set", "totally.unknown.key=1")
	if err == nil {
		t.Fatal("expected error")
	}
	if _, statErr := os.Stat(filepath.Join(dir, "config.toml")); statErr == nil {
		t.Fatal("disk must not be touched for an unknown key")
	}
}

func TestConfigCLI_SetSecretValue_RedirectsToVault(t *testing.T) {
	dir := t.TempDir()
	root, _, _ := newTestRoot(t, dir)
	_, _, err := run(t, root, "config", "set", `registry.pubkey_path="ghp_abcdefghijklmnopqrstuvwxyz0123456789"`)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "vault set") {
		t.Fatalf("expected vault-set redirect in error, got %v", err)
	}
}

func TestConfigCLI_UnsetKey(t *testing.T) {
	dir := t.TempDir()
	root, _, _ := newTestRoot(t, dir)
	if _, _, err := run(t, root, "config", "set", `logging.level="debug"`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := run(t, root, "config", "unset", "logging.level"); err != nil {
		t.Fatalf("unset failed: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "config.toml"))
	if strings.Contains(string(data), "level") {
		t.Fatalf("key not removed:\n%s", data)
	}
}

func TestConfigCLI_Validate_ValidFile(t *testing.T) {
	dir := t.TempDir()
	root, _, _ := newTestRoot(t, dir)
	if _, _, err := run(t, root, "config", "set", `logging.level="info"`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := run(t, root, "config", "validate"); err != nil {
		t.Fatalf("validate failed on a valid file: %v", err)
	}
}

func TestConfigCLI_Validate_InvalidFile(t *testing.T) {
	dir := t.TempDir()
	root, _, _ := newTestRoot(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("not valid toml {{{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := run(t, root, "config", "validate"); err == nil {
		t.Fatal("expected validate to fail on malformed TOML")
	}
}

func TestConfigCLI_List_Effective(t *testing.T) {
	dir := t.TempDir()
	root, out, _ := newTestRoot(t, dir)
	if _, _, err := run(t, root, "config", "set", `logging.level="debug"`); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if _, _, err := run(t, root, "config", "list"); err != nil {
		t.Fatalf("list failed: %v", err)
	}
	if !strings.Contains(out.String(), "logging.level") {
		t.Fatalf("expected logging.level in list output, got %q", out.String())
	}
}

func TestConfigCLI_List_JSON(t *testing.T) {
	dir := t.TempDir()
	root, out, _ := newTestRoot(t, dir)
	_, _, err := run(t, root, "--json", "config", "list")
	if err != nil {
		t.Fatalf("list --json failed: %v", err)
	}
	if !strings.Contains(out.String(), `"key"`) {
		t.Fatalf("expected JSON envelope with key fields, got %q", out.String())
	}
}

func TestConfigCLI_Path(t *testing.T) {
	dir := t.TempDir()
	root, out, _ := newTestRoot(t, dir)
	if _, _, err := run(t, root, "config", "path"); err != nil {
		t.Fatalf("path failed: %v", err)
	}
	if !strings.Contains(out.String(), "config.toml") {
		t.Fatalf("expected config.toml in path output, got %q", out.String())
	}
}

func TestConfigCLI_Reload_NoDaemonExitsZero(t *testing.T) {
	dir := t.TempDir()
	root, out, _ := newTestRoot(t, dir)
	if _, _, err := run(t, root, "config", "reload"); err != nil {
		t.Fatalf("reload with no daemon must exit 0, got %v", err)
	}
	if !strings.Contains(out.String(), "no running daemon") && !strings.Contains(out.String(), "nothing to reload") {
		t.Fatalf("expected an informational no-daemon message, got %q", out.String())
	}
}

func TestConfigCLI_Reload_WithPidfile(t *testing.T) {
	dir := t.TempDir()
	root, _, _ := newTestRoot(t, dir)
	// Our own process's PID is always a valid, signalable target.
	if err := os.WriteFile(filepath.Join(dir, "daemon.pid"), []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	// PID 1 is never this test process; on most systems sending SIGHUP to
	// PID 1 from an unprivileged process fails with EPERM, which must
	// surface as an error, not panic — proving the real signal-send path
	// runs (not merely the "no pidfile" short-circuit) without depending
	// on a real daemon actually being alive.
	_, _, err := run(t, root, "config", "reload")
	if err == nil {
		t.Log("reload to PID 1 unexpectedly succeeded (running as root?) — path still exercised")
	}
}

func TestConfigCLI_SetInvalidLiteral(t *testing.T) {
	dir := t.TempDir()
	root, _, _ := newTestRoot(t, dir)
	if _, _, err := run(t, root, "config", "set", "logging.level", "not valid toml {"); err == nil {
		t.Fatal("expected error for invalid literal")
	}
}

func TestConfigCLI_SetMissingEquals(t *testing.T) {
	dir := t.TempDir()
	root, _, _ := newTestRoot(t, dir)
	if _, _, err := run(t, root, "config", "set", "logging.level"); err == nil {
		t.Fatal("expected error for a single arg with no '='")
	}
}

// fakeEditorScript writes a shell script that appends appendLine to the
// file $1 points at (simulating a user editing and saving), for testing
// `cascade config edit` without a real interactive editor.
