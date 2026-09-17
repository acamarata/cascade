package initconfig

// Purpose: the loader's credential refusal (P1-E16-W4-S35-T7), over the
//   REAL H/S-15.T3 detector.
// Constraints: the scanner under test is the shipped one — a fake would
//   assert only that this file calls something, which is the one thing
//   never in doubt.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

// writeConfig plants a setup file and returns its path.
func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cascade-init.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing the setup file: %v", err)
	}
	return path
}

// realScanner builds the shipped detector.
func realScanner(t *testing.T) SecretScanner {
	t.Helper()
	s, err := NewSecretScanner()
	if err != nil {
		t.Fatalf("NewSecretScanner: %v", err)
	}
	return s
}

// TestLoadAcceptsAFileThatOnlyNamesVariables is the happy path, and the
// half proving the refusal below is not simply refusing everything: a
// document full of env-var NAMES carries no credential.
func TestLoadAcceptsAFileThatOnlyNamesVariables(t *testing.T) {
	path := writeConfig(t, validConfig)

	cfg, err := Load(path, realScanner(t))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Providers) != 1 || cfg.Providers[0].KeyEnv != "ANTHROPIC_API_KEY" {
		t.Errorf("providers = %+v", cfg.Providers)
	}
}

// TestLoadRefusesAFileCarryingACredential is what stands between a
// committed setup file and a committed key.
func TestLoadRefusesAFileCarryingACredential(t *testing.T) {
	path := writeConfig(t, `
schema = "cascade.init/v1"
[[providers]]
name = "anthropic"
auth = "key-env"
key_env = "sk-ant-api03-`+strings.Repeat("A", 88)+`"
`)

	_, err := Load(path, realScanner(t))
	if err == nil {
		t.Fatal("Load accepted a setup file with a key written into it")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
		t.Errorf("kind = %v (typed %t), want KindInvalidInput", kind, ok)
	}
	if !strings.Contains(err.Error(), "vault set") {
		t.Errorf("the refusal does not say what to do instead: %v", err)
	}
	// The refusal must not quote the value. An error message carrying a
	// secret copies it into a terminal, a CI log and a support ticket.
	if strings.Contains(err.Error(), "sk-ant-api03") {
		t.Errorf("the refusal quotes the credential it found: %v", err)
	}
}

// TestACredentialIsRefusedEvenInAMalformedFile pins the ORDER: scan
// first, parse second. A malformed file carrying a key is still a file
// carrying a key, and reporting the TOML error first sends its author to
// fix the wrong thing.
func TestACredentialIsRefusedEvenInAMalformedFile(t *testing.T) {
	path := writeConfig(t, "[[[ this is not toml\nkey = \"sk-ant-api03-"+
		strings.Repeat("A", 88)+"\"\n")

	_, err := Load(path, realScanner(t))
	if err == nil {
		t.Fatal("Load accepted a malformed file carrying a credential")
	}
	if !strings.Contains(err.Error(), "credential material") {
		t.Errorf("the credential was reported second, behind the syntax error: %v", err)
	}
}

// TestLoadRefusesWithoutAScanner is Article 1's rule that absence is
// never a pass: this check is the only thing standing between a committed
// setup file and a committed credential, so an unscanned read is refused
// rather than allowed.
func TestLoadRefusesWithoutAScanner(t *testing.T) {
	path := writeConfig(t, validConfig)

	_, err := Load(path, nil)
	if err == nil {
		t.Fatal("Load read a setup file it could not scan")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInternal {
		t.Errorf("kind = %v (typed %t), want KindInternal", kind, ok)
	}
}

// TestLoadRefusesAMissingFile names the path, because a typo'd --config
// is the likeliest way to reach this.
func TestLoadRefusesAMissingFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope.toml")

	_, err := Load(missing, realScanner(t))
	if err == nil {
		t.Fatal("Load succeeded on a file that does not exist")
	}
	if !strings.Contains(err.Error(), missing) {
		t.Errorf("the refusal does not name the path: %v", err)
	}
}

// TestTheScannerSeamMatchesTheRealDetector states the compile-time proof
// as a test: the interface this package declares is the method the
// shipped detector actually has, so the seam cannot drift into a shape
// only a fake satisfies.
func TestTheScannerSeamMatchesTheRealDetector(t *testing.T) {
	detector, err := secrets.NewDetector(secrets.DefaultRegistry(), secrets.DefaultDetectionConfig())
	if err != nil {
		t.Fatalf("NewDetector: %v", err)
	}
	var seam SecretScanner = detector
	if hits := seam.ScanCertain([]byte("nothing to see")); len(hits) != 0 {
		t.Errorf("the real detector reported %d hit(s) in ordinary prose", len(hits))
	}
}
