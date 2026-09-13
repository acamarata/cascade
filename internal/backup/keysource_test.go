// Purpose: default-lane unit coverage for keysource.go's shared
// resolution logic (resolveKeySource, manifestSigningKeyFromVault,
// ageIdentityFromVault) -- the vault-vs-env custody decision that
// DEFECT-backup-keys-vault-not-read.md's fix added. These are ordinary
// in-process calls against memVault/errVault (recovery_test.go,
// recovery_vault_test.go), never real ssh/network/binary, so they belong
// in the default lane rather than behind the integration tag.
// SPORT: internal.backup.keysource/ADD (tests) (FIX-backup-default-lane-coverage).
package backup

import (
	"context"
	"crypto/ed25519"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestResolveKeySource_VaultAnswers asserts vault precedence: once an
// entry exists, it answers even when the caller also reports envSet, per
// keysource.go's stated "once escrowed, vault is authoritative" order.
func TestResolveKeySource_VaultAnswers(t *testing.T) {
	ctx := context.Background()
	vault := newMemVault()
	vault.entries["k"] = []byte("present")
	source, found, err := resolveKeySource(ctx, vault, "k", true)
	if err != nil {
		t.Fatalf("resolveKeySource: %v", err)
	}
	if !found || source != KeySourceVault {
		t.Fatalf("resolveKeySource(vault entry, envSet) = (%q, %v), want (%q, true)", source, found, KeySourceVault)
	}
}

// TestResolveKeySource_VaultEmptyFallsBackToEnv asserts the STATED
// fallback: a vault with no entry for this name, but the caller's env
// var set, answers env_fallback rather than refusing.
func TestResolveKeySource_VaultEmptyFallsBackToEnv(t *testing.T) {
	ctx := context.Background()
	vault := newMemVault()
	source, found, err := resolveKeySource(ctx, vault, "missing", true)
	if err != nil {
		t.Fatalf("resolveKeySource: %v", err)
	}
	if !found || source != KeySourceEnvFallback {
		t.Fatalf("resolveKeySource(no vault entry, envSet) = (%q, %v), want (%q, true)", source, found, KeySourceEnvFallback)
	}
}

// TestResolveKeySource_BothAbsentRefuses asserts the "neither" case: a
// vault present but without this entry, and no env var set, reports
// found=false so the caller returns its own typed missing-key sentinel.
func TestResolveKeySource_BothAbsentRefuses(t *testing.T) {
	ctx := context.Background()
	vault := newMemVault()
	source, found, err := resolveKeySource(ctx, vault, "missing", false)
	if err != nil {
		t.Fatalf("resolveKeySource: %v", err)
	}
	if found || source != "" {
		t.Fatalf("resolveKeySource(no vault entry, no env) = (%q, %v), want (\"\", false)", source, found)
	}
}

// TestResolveKeySource_NilVaultUsesEnv asserts a nil vault (no elevated
// broker at the call site) is treated exactly like an empty vault: env,
// when set, still answers.
func TestResolveKeySource_NilVaultUsesEnv(t *testing.T) {
	ctx := context.Background()
	source, found, err := resolveKeySource(ctx, nil, "k", true)
	if err != nil {
		t.Fatalf("resolveKeySource: %v", err)
	}
	if !found || source != KeySourceEnvFallback {
		t.Fatalf("resolveKeySource(nil vault, envSet) = (%q, %v), want (%q, true)", source, found, KeySourceEnvFallback)
	}
	if _, found, _ := resolveKeySource(ctx, nil, "k", false); found {
		t.Fatal("resolveKeySource(nil vault, no env) reported found, want false")
	}
}

// TestResolveKeySource_ExistsErrorPropagates asserts a vault Exists
// failure is returned to the caller, never swallowed into "fall through
// to env" -- a vault that is present but erroring is not the same as a
// vault with no entry (keysource.go's own Constraints section).
func TestResolveKeySource_ExistsErrorPropagates(t *testing.T) {
	ctx := context.Background()
	_, found, err := resolveKeySource(ctx, errVault{failExists: true}, "k", true)
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("resolveKeySource(Exists error) err = %v, want KindUnavailable", err)
	}
	if found {
		t.Fatal("resolveKeySource(Exists error) reported found, want the error surfaced instead")
	}
}

// TestManifestSigningKeyFromVault_RoundTrip proves the vault-held seed is
// reconstituted into the exact same Ed25519 key EnsureManifestSigningKey
// stored, matched against the derived public half.
func TestManifestSigningKeyFromVault_RoundTrip(t *testing.T) {
	ctx := context.Background()
	vault := newMemVault()
	wantPub, err := EnsureManifestSigningKey(ctx, vault)
	if err != nil {
		t.Fatalf("EnsureManifestSigningKey: %v", err)
	}
	priv, err := manifestSigningKeyFromVault(ctx, vault)
	if err != nil {
		t.Fatalf("manifestSigningKeyFromVault: %v", err)
	}
	gotPub, ok := priv.Public().(ed25519.PublicKey)
	if !ok || !gotPub.Equal(wantPub) {
		t.Fatal("manifestSigningKeyFromVault returned a key whose public half does not match the escrowed one")
	}
}

// TestManifestSigningKeyFromVault_WrongLengthRefuses asserts the seed
// length check fails closed with KindIntegrity, never a silent short key.
func TestManifestSigningKeyFromVault_WrongLengthRefuses(t *testing.T) {
	ctx := context.Background()
	vault := newMemVault()
	vault.entries[ManifestSigningKeyVaultName] = []byte("too-short")
	if _, err := manifestSigningKeyFromVault(ctx, vault); !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("manifestSigningKeyFromVault(short seed) err = %v, want KindIntegrity", err)
	}
}

// TestManifestSigningKeyFromVault_GetErrorPropagates asserts a vault Get
// failure is returned, not swallowed.
func TestManifestSigningKeyFromVault_GetErrorPropagates(t *testing.T) {
	ctx := context.Background()
	if _, err := manifestSigningKeyFromVault(ctx, errVault{existsValue: true, failGet: true}); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("manifestSigningKeyFromVault(Get error) err = %v, want KindUnavailable", err)
	}
}

// TestAgeIdentityFromVault_RoundTrip proves the vault-held identity
// string round-trips through ageIdentityFromVault unchanged and parses.
func TestAgeIdentityFromVault_RoundTrip(t *testing.T) {
	ctx := context.Background()
	identity, _ := newTestAgeKeypair(t)
	vault := newMemVault()
	if err := vault.Set(ctx, AgeIdentityVaultName, []byte(identity)); err != nil {
		t.Fatalf("vault.Set: %v", err)
	}
	got, err := ageIdentityFromVault(ctx, vault)
	if err != nil {
		t.Fatalf("ageIdentityFromVault: %v", err)
	}
	if got != identity {
		t.Fatal("ageIdentityFromVault did not return the escrowed identity unchanged")
	}
}

// TestAgeIdentityFromVault_MalformedRefuses asserts a vault entry that
// does not parse as an X25519 identity fails closed here, not several
// steps later at the first Decrypt call.
func TestAgeIdentityFromVault_MalformedRefuses(t *testing.T) {
	ctx := context.Background()
	vault := newMemVault()
	vault.entries[AgeIdentityVaultName] = []byte("not-an-age-identity")
	if _, err := ageIdentityFromVault(ctx, vault); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("ageIdentityFromVault(malformed) err = %v, want KindInvalidInput", err)
	}
}

// TestAgeIdentityFromVault_GetErrorPropagates asserts a vault Get failure
// is returned, not swallowed.
func TestAgeIdentityFromVault_GetErrorPropagates(t *testing.T) {
	if _, err := ageIdentityFromVault(context.Background(), errVault{existsValue: true, failGet: true}); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("ageIdentityFromVault(Get error) err = %v, want KindUnavailable", err)
	}
}
