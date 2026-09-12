// Purpose: the S-42.T6 ceremony's vault-custody tests -- split out of
// recovery_test.go to stay under the repo's 300-line-per-file gate.
// EnsureAgeIdentity/EnsureManifestSigningKey idempotence, the R-14.58
// custody split (private seed never returned), wrong-seed-length
// rejection, and every collaborator-error propagation path via errVault.
// SPORT: internal.backup.recovery/ADD (tests) (P1-E19-W4-S42-T6).
package backup

import (
	"bytes"
	"context"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestEnsureAgeIdentity_IdempotentAcrossCalls(t *testing.T) {
	ctx := context.Background()
	vault := newMemVault()
	first, err := EnsureAgeIdentity(ctx, vault)
	if err != nil {
		t.Fatalf("EnsureAgeIdentity (first): %v", err)
	}
	second, err := EnsureAgeIdentity(ctx, vault)
	if err != nil {
		t.Fatalf("EnsureAgeIdentity (second): %v", err)
	}
	if first != second {
		t.Fatal("EnsureAgeIdentity minted a second identity; this would orphan every snapshot already encrypted to the first")
	}
}

// TestEnsureManifestSigningKey_CustodySplit asserts the R-14.58 custody
// split: the private seed is stored ONLY under the vault entry name, and
// EnsureManifestSigningKey never returns it -- only the derived pubkey.
func TestEnsureManifestSigningKey_CustodySplit(t *testing.T) {
	ctx := context.Background()
	vault := newMemVault()
	pub, err := EnsureManifestSigningKey(ctx, vault)
	if err != nil {
		t.Fatalf("EnsureManifestSigningKey (first): %v", err)
	}
	if len(pub) == 0 {
		t.Fatal("EnsureManifestSigningKey returned an empty pubkey")
	}
	seed, ok := vault.entries[ManifestSigningKeyVaultName]
	if !ok || len(seed) == 0 {
		t.Fatal("the private seed was not stored under ManifestSigningKeyVaultName")
	}
	// Idempotent: a second call derives the SAME pubkey from the SAME
	// vault-held seed rather than minting a new keypair, which would
	// invalidate every manifest already signed with the first.
	again, err := EnsureManifestSigningKey(ctx, vault)
	if err != nil {
		t.Fatalf("EnsureManifestSigningKey (second): %v", err)
	}
	if !bytes.Equal(pub, again) {
		t.Fatal("EnsureManifestSigningKey minted a second signing key; this would invalidate every manifest already signed")
	}
}

// errVault is a VaultStore test double that fails closed on whichever
// method name is configured, proving EnsureAgeIdentity/
// EnsureManifestSigningKey propagate every collaborator failure rather
// than swallowing it.
type errVault struct {
	failExists, failGet, failSet bool
	existsValue                  bool
}

func (v errVault) Exists(context.Context, string) (bool, error) {
	if v.failExists {
		return false, cascade.New(cascade.KindUnavailable, "errVault: Exists failed")
	}
	return v.existsValue, nil
}

func (v errVault) Get(context.Context, string) ([]byte, error) {
	if v.failGet {
		return nil, cascade.New(cascade.KindUnavailable, "errVault: Get failed")
	}
	return []byte("stub"), nil
}

func (v errVault) Set(context.Context, string, []byte) error {
	if v.failSet {
		return cascade.New(cascade.KindUnavailable, "errVault: Set failed")
	}
	return nil
}

func TestEnsureAgeIdentity_PropagatesCollaboratorErrors(t *testing.T) {
	ctx := context.Background()
	if _, err := EnsureAgeIdentity(ctx, errVault{failExists: true}); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("EnsureAgeIdentity(Exists error) = %v, want KindUnavailable", err)
	}
	if _, err := EnsureAgeIdentity(ctx, errVault{existsValue: true, failGet: true}); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("EnsureAgeIdentity(Get error) = %v, want KindUnavailable", err)
	}
	if _, err := EnsureAgeIdentity(ctx, errVault{failSet: true}); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("EnsureAgeIdentity(Set error, minting a fresh identity) = %v, want KindUnavailable", err)
	}
}

func TestEnsureManifestSigningKey_PropagatesCollaboratorErrors(t *testing.T) {
	ctx := context.Background()
	if _, err := EnsureManifestSigningKey(ctx, errVault{failExists: true}); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("EnsureManifestSigningKey(Exists error) = %v, want KindUnavailable", err)
	}
	if _, err := EnsureManifestSigningKey(ctx, errVault{existsValue: true, failGet: true}); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("EnsureManifestSigningKey(Get error) = %v, want KindUnavailable", err)
	}
	if _, err := EnsureManifestSigningKey(ctx, errVault{failSet: true}); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("EnsureManifestSigningKey(Set error, minting a fresh keypair) = %v, want KindUnavailable", err)
	}
}

func TestEnsureManifestSigningKey_WrongSeedLengthRefuses(t *testing.T) {
	ctx := context.Background()
	vault := newMemVault()
	vault.entries[ManifestSigningKeyVaultName] = []byte("too-short")
	if _, err := EnsureManifestSigningKey(ctx, vault); !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("EnsureManifestSigningKey(malformed seed) error kind = %v, want KindIntegrity", err)
	}
}
