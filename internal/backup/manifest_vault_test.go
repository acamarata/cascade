// Purpose: ManifestSigningKey's vault-custody paths -- split out of
// manifest_test.go (which already covers the env-only paths) to stay
// under the repo's 300-line-per-file gate, matching recovery_vault_test.go's
// precedent for the analogous recovery.go split. Proves the reported
// KeySource matches whichever custody actually answered.
// SPORT: internal.backup.manifest/ADD (tests) (FIX-backup-default-lane-coverage).
package backup

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestManifestSigningKey_VaultAnswersEvenWithEnvSet asserts vault
// precedence at the exported entry point: once escrowed, the vault
// answers even when a (different, stale) env value is also present.
func TestManifestSigningKey_VaultAnswersEvenWithEnvSet(t *testing.T) {
	ctx := context.Background()
	t.Setenv(ManifestSigningKeyEnvVar, base64.StdEncoding.EncodeToString(make([]byte, 32)))
	vault := newMemVault()
	wantPub, err := EnsureManifestSigningKey(ctx, vault)
	if err != nil {
		t.Fatalf("EnsureManifestSigningKey: %v", err)
	}
	priv, source, err := ManifestSigningKey(ctx, vault)
	if err != nil {
		t.Fatalf("ManifestSigningKey: %v", err)
	}
	if source != KeySourceVault {
		t.Fatalf("source = %q, want %q", source, KeySourceVault)
	}
	gotPub, ok := priv.Public().(ed25519.PublicKey)
	if !ok || !bytes.Equal(gotPub, wantPub) {
		t.Fatal("ManifestSigningKey(vault escrowed) returned a key not matching the escrowed pubkey")
	}
}

// TestManifestSigningKey_VaultEmptyFallsBackToEnv asserts a vault with no
// entry for this key still lets the env-var fallback answer, reported
// honestly as env_fallback.
func TestManifestSigningKey_VaultEmptyFallsBackToEnv(t *testing.T) {
	ctx := context.Background()
	seed := make([]byte, 32)
	t.Setenv(ManifestSigningKeyEnvVar, base64.StdEncoding.EncodeToString(seed))
	_, source, err := ManifestSigningKey(ctx, newMemVault())
	if err != nil {
		t.Fatalf("ManifestSigningKey: %v", err)
	}
	if source != KeySourceEnvFallback {
		t.Fatalf("source = %q, want %q", source, KeySourceEnvFallback)
	}
}

// TestManifestSigningKey_BothAbsentRefusesWithVaultPresent asserts the
// existing typed missing-key sentinel is unchanged when a vault is
// supplied but empty and no env var is set -- not a different error.
func TestManifestSigningKey_BothAbsentRefusesWithVaultPresent(t *testing.T) {
	ctx := context.Background()
	t.Setenv(ManifestSigningKeyEnvVar, "")
	if _, _, err := ManifestSigningKey(ctx, newMemVault()); err != ErrManifestSigningKeyMissing {
		t.Fatalf("ManifestSigningKey(vault present+empty, no env) = %v, want ErrManifestSigningKeyMissing", err)
	}
}

// TestManifestSigningKey_VaultExistsErrorPropagates asserts a vault
// collaborator failure is returned as itself, never masked as the
// missing-key sentinel -- a vault that is present but erroring is not
// the same as no key configured at all.
func TestManifestSigningKey_VaultExistsErrorPropagates(t *testing.T) {
	ctx := context.Background()
	t.Setenv(ManifestSigningKeyEnvVar, base64.StdEncoding.EncodeToString(make([]byte, 32)))
	_, _, err := ManifestSigningKey(ctx, errVault{failExists: true})
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("ManifestSigningKey(vault Exists error) err = %v, want KindUnavailable", err)
	}
	if err == ErrManifestSigningKeyMissing {
		t.Fatal("ManifestSigningKey(vault Exists error) was masked as the missing-key sentinel")
	}
}
