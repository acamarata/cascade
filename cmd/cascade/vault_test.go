package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

// testVaultDeps builds a vault command tree over a file vault in
// t.TempDir(), so no test touches the real keychain or the real data dir.
func testVaultDeps(t *testing.T, gate secrets.ElevationGate, env map[string]string) vaultDeps {
	t.Helper()
	dir := t.TempDir()
	return vaultDeps{
		Getenv: func(k string) string { return env[k] },
		NewCustody: func() (secrets.Custody, error) {
			return secrets.SelectCustody(secrets.Config{
				Service:    "cascade-cli-test",
				Dir:        dir,
				Passphrase: "cli-test-pass",
				Runner:     alwaysFailRunner,
			})
		},
		Gate:     gate,
		ReadFile: os.ReadFile,
	}
}

// alwaysFailRunner forces the darwin backend to report unavailable, so the
// CLI tests always land on the hermetic temp-dir file vault.
func alwaysFailRunner(context.Context, string, ...string) ([]byte, error) {
	return nil, os.ErrNotExist
}

type okGate struct{}

func (okGate) Authorize(context.Context, string) error { return nil }

type refusingGate struct{}

func (refusingGate) Authorize(_ context.Context, verb string) error {
	return cascade.Newf(cascade.KindElevationRequired, "ELEVATION_REQUIRED: %s needs local presence", verb)
}

// runVault executes one vault command against an isolated tree and returns
// stdout, stderr and the error.
func runVault(t *testing.T, deps vaultDeps, stdin string, args ...string) (string, string, error) {
	t.Helper()
	root := &cobra.Command{Use: "cascade", SilenceUsage: true, SilenceErrors: true}
	root.PersistentFlags().Bool("json", false, "")
	root.PersistentFlags().Bool("quiet", false, "")
	root.PersistentFlags().Bool("verbose", false, "")
	root.PersistentFlags().Bool("no-color", false, "")
	cmd := newVaultCmd(deps)
	guardUnknownSubcommands(cmd)
	root.AddCommand(cmd)

	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs(append([]string{"vault"}, args...))
	err := root.ExecuteContext(context.Background())
	return stdout.String(), stderr.String(), err
}

func TestVaultCLISetAndList(t *testing.T) {
	deps := testVaultDeps(t, okGate{}, nil)
	if _, _, err := runVault(t, deps, "s3cr3t\n", "set", "API_TOKEN"); err != nil {
		t.Fatalf("set: %v", err)
	}
	stdout, stderr, err := runVault(t, deps, "", "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(stdout, "API_TOKEN") {
		t.Fatalf("list stdout = %q, want the name", stdout)
	}
	if strings.Contains(stdout, "s3cr3t") || strings.Contains(stderr, "s3cr3t") {
		t.Fatal("list emitted the secret value")
	}
}

// TestVaultCLIListEmitsNamesOnly is the acceptance criterion's table test:
// three stored secrets, and no value byte on stdout or stderr, in either
// output mode.
func TestVaultCLIListEmitsNamesOnly(t *testing.T) {
	deps := testVaultDeps(t, okGate{}, nil)
	stored := map[string]string{
		"ALPHA":   "value-alpha-canary",
		"BRAVO":   "value-bravo-canary",
		"CHARLIE": "value-charlie-canary",
	}
	for name, value := range stored {
		if _, _, err := runVault(t, deps, value+"\n", "set", name); err != nil {
			t.Fatalf("set %s: %v", name, err)
		}
	}
	for _, mode := range [][]string{{"list"}, {"list", "--json"}} {
		stdout, stderr, err := runVault(t, deps, "", mode...)
		if err != nil {
			t.Fatalf("%v: %v", mode, err)
		}
		for name, value := range stored {
			if !strings.Contains(stdout, name) {
				t.Fatalf("%v: stdout %q is missing %s", mode, stdout, name)
			}
			if strings.Contains(stdout+stderr, value) {
				t.Fatalf("%v: output carries the value of %s", mode, name)
			}
		}
	}
	stdout, _, err := runVault(t, deps, "", "list", "--json")
	if err != nil {
		t.Fatalf("list --json: %v", err)
	}
	var envelope struct {
		Data listView `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("list --json is not a JSON envelope: %v (%q)", err, stdout)
	}
	if len(envelope.Data.Names) != 3 {
		t.Fatalf("list --json names = %v", envelope.Data.Names)
	}
}

func TestVaultCLISetCollisionNoInput(t *testing.T) {
	deps := testVaultDeps(t, okGate{}, map[string]string{"CASCADE_NO_INPUT": "1"})
	if _, _, err := runVault(t, deps, "first\n", "set", "TOKEN"); err != nil {
		t.Fatalf("first set: %v", err)
	}
	stdout, _, err := runVault(t, deps, "second\n", "set", "TOKEN")
	if err != nil {
		t.Fatalf("second set: %v", err)
	}
	if !strings.Contains(stdout, "TOKEN") || strings.Contains(stdout, "TOKEN_2") {
		t.Fatalf("non-interactive collision produced %q, want an in-place update", stdout)
	}
	names, _, err := runVault(t, deps, "", "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if strings.Count(names, "TOKEN") != 1 {
		t.Fatalf("list = %q, want exactly one entry", names)
	}
}

func TestVaultCLISetCollisionInteractive(t *testing.T) {
	deps := testVaultDeps(t, okGate{}, nil)
	if _, _, err := runVault(t, deps, "first\n", "set", "TOKEN"); err != nil {
		t.Fatalf("first set: %v", err)
	}
	// "rename" saves alongside; the prompt text names the suggestion.
	stdout, stderr, err := runVault(t, deps, "rename\nsecond\n", "set", "TOKEN")
	if err != nil {
		t.Fatalf("interactive rename: %v", err)
	}
	prompt := stdout + stderr
	if !strings.Contains(prompt, "TOKEN exists") || !strings.Contains(prompt, "TOKEN_2") {
		t.Fatalf("prompt = %q, want the update-or-rename question", prompt)
	}
	if !strings.Contains(stdout, "TOKEN_2") {
		t.Fatalf("rename result = %q, want TOKEN_2", stdout)
	}
	// "update" overwrites in place.
	stdout, _, err = runVault(t, deps, "update\nthird\n", "set", "TOKEN")
	if err != nil {
		t.Fatalf("interactive update: %v", err)
	}
	if strings.Contains(stdout, "TOKEN_3") {
		t.Fatalf("update result = %q, want an in-place update", stdout)
	}
	// An unrecognised answer refuses and writes nothing.
	if _, _, err := runVault(t, deps, "maybe\n", "set", "TOKEN"); !isCLIKind(err, cascade.KindInvalidInput) {
		t.Fatalf("an unrecognised answer = %v, want a refusal", err)
	}
}

func TestVaultCLISetValueFile(t *testing.T) {
	deps := testVaultDeps(t, okGate{}, map[string]string{"CASCADE_NO_INPUT": "1"})
	path := filepath.Join(t.TempDir(), "token.txt")
	if err := os.WriteFile(path, []byte("from-file\n"), 0o600); err != nil {
		t.Fatalf("writing: %v", err)
	}
	if _, _, err := runVault(t, deps, "", "set", "TOKEN", "--value-file", path); err != nil {
		t.Fatalf("set --value-file: %v", err)
	}
	stdout, _, err := runVault(t, deps, "", "get", "TOKEN")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if stdout != "from-file" {
		t.Fatalf("get = %q, want the file's contents with the trailing newline trimmed", stdout)
	}
	if _, _, err := runVault(t, deps, "", "set", "TOKEN", "--value-file", path+".missing"); !isCLIKind(err, cascade.KindNotFound) {
		t.Fatalf("a missing value file = %v", err)
	}
}

func TestVaultCLIRejectsBadName(t *testing.T) {
	deps := testVaultDeps(t, okGate{}, nil)
	if _, _, err := runVault(t, deps, "v\n", "set", "bad name"); !isCLIKind(err, cascade.KindInvalidInput) {
		t.Fatalf("set with a bad name = %v", err)
	}
}

func TestVaultCLIUnknownSubcommand(t *testing.T) {
	deps := testVaultDeps(t, okGate{}, nil)
	if _, _, err := runVault(t, deps, "", "bogus"); !isCLIKind(err, cascade.KindInvalidInput) {
		t.Fatalf("an unknown subcommand = %v", err)
	}
}

func TestVaultCLIBrokerConstructionFailure(t *testing.T) {
	deps := vaultDeps{
		Getenv:     func(string) string { return "" },
		NewCustody: func() (secrets.Custody, error) { return nil, cascade.New(cascade.KindUnavailable, "no store") },
		Gate:       okGate{},
		ReadFile:   os.ReadFile,
	}
	if _, _, err := runVault(t, deps, "", "list"); !isCLIKind(err, cascade.KindUnavailable) {
		t.Fatalf("a store that will not open = %v", err)
	}
	bare := vaultDeps{Getenv: func(string) string { return "" }}
	if _, _, err := runVault(t, bare, "", "list"); !isCLIKind(err, cascade.KindInternal) {
		t.Fatalf("a tree with no custody provider = %v", err)
	}
}

// isCLIKind reports whether err carries the given taxonomy kind.
func isCLIKind(err error, want cascade.Kind) bool {
	got, ok := cascade.KindOf(err)
	return ok && got == want
}

// TestVaultMountedOnRealRoot drives the production command tree, not a
// test-local one: it is the assertion that this subsystem is reachable
// from the shipped binary rather than only from its own tests.
func TestVaultMountedOnRealRoot(t *testing.T) {
	root := newRootCmd()
	var vault *cobra.Command
	for _, sub := range root.Commands() {
		if sub.Name() == "vault" {
			vault = sub
		}
	}
	if vault == nil {
		t.Fatal("`vault` is not mounted on the real root command")
	}
	want := map[string]bool{"set": false, "get": false, "list": false, "rotate": false, "import": false, "audit": false}
	for _, sub := range vault.Commands() {
		if _, ok := want[sub.Name()]; ok {
			want[sub.Name()] = true
		}
		if sub.Long == "" || sub.Example == "" {
			t.Fatalf("vault %s has no Long/Example help text (it is the generated CLI docs source)", sub.Name())
		}
	}
	for name, found := range want {
		if !found {
			t.Fatalf("vault %s is not mounted", name)
		}
	}
}
