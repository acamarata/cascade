// Purpose: AgeIdentity's vault-custody paths -- split out of
// integrity_test.go (which already covers the env-only paths) to stay
// under the repo's 300-line-per-file gate. Proves the reported KeySource
// matches whichever custody actually answered.
// SPORT: internal.backup.integrity/ADD (tests) (FIX-backup-default-lane-coverage).
package backup

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestAgeIdentity_VaultAnswersEvenWithEnvSet asserts vault precedence at
// the exported entry point: once escrowed, the vault answers even when a
// (different, stale) env value is also present.
func TestAgeIdentity_VaultAnswersEvenWithEnvSet(t *testing.T) {
	ctx := context.Background()
	staleIdentity, _ := newTestAgeKeypair(t)
	t.Setenv(AgeIdentityEnvVar, staleIdentity)
	vault := newMemVault()
	wantIdentity, err := EnsureAgeIdentity(ctx, vault)
	if err != nil {
		t.Fatalf("EnsureAgeIdentity: %v", err)
	}
	got, source, err := AgeIdentity(ctx, vault)
	if err != nil {
		t.Fatalf("AgeIdentity: %v", err)
	}
	if source != KeySourceVault {
		t.Fatalf("source = %q, want %q", source, KeySourceVault)
	}
	if got != wantIdentity {
		t.Fatal("AgeIdentity(vault escrowed) returned the stale env identity instead of the escrowed one")
	}
}

// TestAgeIdentity_VaultEmptyFallsBackToEnv asserts a vault with no entry
// still lets the env-var fallback answer, reported honestly.
func TestAgeIdentity_VaultEmptyFallsBackToEnv(t *testing.T) {
	ctx := context.Background()
	identity, _ := newTestAgeKeypair(t)
	t.Setenv(AgeIdentityEnvVar, identity)
	got, source, err := AgeIdentity(ctx, newMemVault())
	if err != nil {
		t.Fatalf("AgeIdentity: %v", err)
	}
	if source != KeySourceEnvFallback {
		t.Fatalf("source = %q, want %q", source, KeySourceEnvFallback)
	}
	if got != identity {
		t.Fatal("AgeIdentity(env fallback) did not return the env identity unchanged")
	}
}

// TestAgeIdentity_BothAbsentRefusesWithVaultPresent asserts the existing
// typed missing-key sentinel is unchanged when a vault is supplied but
// empty and no env var is set.
func TestAgeIdentity_BothAbsentRefusesWithVaultPresent(t *testing.T) {
	ctx := context.Background()
	t.Setenv(AgeIdentityEnvVar, "")
	if _, _, err := AgeIdentity(ctx, newMemVault()); err != ErrAgeIdentityMissing {
		t.Fatalf("AgeIdentity(vault present+empty, no env) = %v, want ErrAgeIdentityMissing", err)
	}
}

// TestAgeIdentity_VaultExistsErrorPropagates asserts a vault collaborator
// failure is returned as itself, never masked as the missing-key
// sentinel.
func TestAgeIdentity_VaultExistsErrorPropagates(t *testing.T) {
	ctx := context.Background()
	identity, _ := newTestAgeKeypair(t)
	t.Setenv(AgeIdentityEnvVar, identity)
	_, _, err := AgeIdentity(ctx, errVault{failExists: true})
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("AgeIdentity(vault Exists error) err = %v, want KindUnavailable", err)
	}
	if err == ErrAgeIdentityMissing {
		t.Fatal("AgeIdentity(vault Exists error) was masked as the missing-key sentinel")
	}
}
