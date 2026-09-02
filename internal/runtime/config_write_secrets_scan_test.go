package runtime

import (
	"errors"
	"testing"
)

// Purpose: tests for ScanTreeForSecrets — the whole-document secret
//   screener added by this ticket's R-14 CR fix (P1-E03-W1-S05-T8,
//   blocking fix 3) so `cascade config edit` gets the same guard `set`
//   already applies to a single literal. Kept as its own file rather
//   than growing config_write_test.go (already 254 lines) past the
//   300-line cap, per R-14.117.
// Inputs: n/a (test-only).
// Outputs: n/a (test-only).
// Constraints: Art.7.1 — no filesystem access needed; tests operate on
//   in-memory trees only.
// SPORT: runtime/config-write-verbs (ADD, placeholder per T-8 sport_updates).

func TestScanTreeForSecrets_FlagsNestedSecretLiteral(t *testing.T) {
	tree := map[string]interface{}{
		"logging": map[string]interface{}{"level": "debug"},
		"registry": map[string]interface{}{
			"pubkey_path": "ghp_abcdefghijklmnopqrstuvwxyz0123456789",
		},
	}
	err := ScanTreeForSecrets(tree)
	if err == nil {
		t.Fatal("expected a *SecretLiteralError, got nil")
	}
	var se *SecretLiteralError
	if !errors.As(err, &se) {
		t.Fatalf("error = %v, want *SecretLiteralError", err)
	}
	if se.Field != "registry.pubkey_path" {
		t.Errorf("Field = %q, want registry.pubkey_path", se.Field)
	}
}

func TestScanTreeForSecrets_CleanTreePasses(t *testing.T) {
	tree := map[string]interface{}{
		"logging": map[string]interface{}{"level": "debug", "format": "json"},
		"runtime": map[string]interface{}{"profile": "local"},
	}
	if err := ScanTreeForSecrets(tree); err != nil {
		t.Fatalf("ScanTreeForSecrets on a clean tree: %v", err)
	}
}

func TestScanTreeForSecrets_NonStringLeavesNeverFlagged(t *testing.T) {
	tree := map[string]interface{}{
		"elevation": map[string]interface{}{"allow_remote": true},
		"retrieval": map[string]interface{}{"fusion": map[string]interface{}{"k": int64(80)}},
	}
	if err := ScanTreeForSecrets(tree); err != nil {
		t.Fatalf("ScanTreeForSecrets flagged a non-string leaf: %v", err)
	}
}
