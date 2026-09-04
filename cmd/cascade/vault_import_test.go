package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// writeEnvFile writes a vault.env fixture under t.TempDir().
func writeEnvFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "vault.env")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}
	return path
}

// TestVaultImportIdempotent is the acceptance criterion: a second import of
// the same file exits 0 with no error output and no duplicate entries.
func TestVaultImportIdempotent(t *testing.T) {
	deps := testVaultDeps(t, okGate{}, map[string]string{"CASCADE_NO_INPUT": "1"})
	path := writeEnvFile(t, "# comment\n\nALPHA=one\nBRAVO=two\n")

	stdout, stderr, err := runVault(t, deps, "", "import", path)
	if err != nil {
		t.Fatalf("first import: %v", err)
	}
	if stderr != "" {
		t.Fatalf("first import wrote to stderr: %q", stderr)
	}
	if !strings.Contains(stdout, "ALPHA") || !strings.Contains(stdout, "BRAVO") {
		t.Fatalf("first import report = %q", stdout)
	}

	_, stderr, err = runVault(t, deps, "", "import", path)
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	if stderr != "" {
		t.Fatalf("second import wrote to stderr: %q", stderr)
	}

	names, _, err := runVault(t, deps, "", "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if strings.Count(names, "ALPHA") != 1 || strings.Count(names, "BRAVO") != 1 {
		t.Fatalf("list after two imports = %q, want one entry each", names)
	}
	if strings.Contains(names, "ALPHA_2") {
		t.Fatal("the second import created a suffixed duplicate")
	}
	// The values arrived intact.
	value, _, err := runVault(t, deps, "", "get", "ALPHA")
	if err != nil || value != "one" {
		t.Fatalf("get ALPHA = %q, %v", value, err)
	}
}

// TestVaultImportNeverEchoesValues covers both the success and the failure
// path: neither may put a value on stdout or stderr.
func TestVaultImportNeverEchoesValues(t *testing.T) {
	deps := testVaultDeps(t, okGate{}, map[string]string{"CASCADE_NO_INPUT": "1"})
	const canary = "canary-secret-value"

	ok := writeEnvFile(t, "ALPHA="+canary+"\n")
	stdout, stderr, err := runVault(t, deps, "", "import", ok)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if strings.Contains(stdout+stderr, canary) {
		t.Fatalf("a successful import echoed the value: %q / %q", stdout, stderr)
	}

	bad := writeEnvFile(t, "ALPHA="+canary+"\nnot-an-assignment\n")
	stdout, stderr, err = runVault(t, deps, "", "import", bad)
	if !isCLIKind(err, cascade.KindInvalidInput) {
		t.Fatalf("a malformed file = %v", err)
	}
	if strings.Contains(stdout+stderr+err.Error(), canary) {
		t.Fatalf("a failed import echoed the value: %v", err)
	}
	if !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("the failure does not name the line: %v", err)
	}
}

func TestVaultImportMissingFile(t *testing.T) {
	deps := testVaultDeps(t, okGate{}, nil)
	if _, _, err := runVault(t, deps, "", "import", filepath.Join(t.TempDir(), "absent.env")); !isCLIKind(err, cascade.KindNotFound) {
		t.Fatalf("a missing import file = %v", err)
	}
	bare := vaultDeps{Getenv: func(string) string { return "" }}
	if _, _, err := runVault(t, bare, "", "import", "anything"); !isCLIKind(err, cascade.KindInternal) {
		t.Fatalf("a tree with no file reader = %v", err)
	}
}
