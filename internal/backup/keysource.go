// Purpose: the DEFECT-backup-keys-vault-not-read.md fix's shared
//	resolution logic: manifest.go's ManifestSigningKey and integrity.go's
//	AgeIdentity both need to answer from the S-42.T6 ceremony's vault
//	first, with the pre-existing environment variable demoted to an
//	explicit, STATED fallback -- never a silent either/or. This file is
//	the one place that decides which custody answers, so the two callers
//	can never independently drift on the order.
//
// Inputs: an optional VaultStore (nil when no elevated broker is
//	available at the call site -- see manifest.go/integrity.go's own
//	callers for when that is and is not the case) and the vault entry
//	name / env var pair for one key.
//
// Outputs: which KeySource will answer, or an honest "neither" so the
//	caller returns its existing typed missing-key sentinel unchanged.
//
// Constraints: a vault existence check never reads or returns key bytes
//	(resolveKeySource only calls Exists); a vault Exists error is
//	propagated, never swallowed into "fall through to env" -- a vault
//	that is present but erroring is not the same as a vault with no
//	entry. The env var is consulted only when the vault has no entry (or
//	no vault was supplied), matching the ceremony's own precedence: once
//	an operator has escrowed a key, that vault entry is authoritative.
//
// SPORT: internal.backup.keysource/ADD (DEFECT-backup-keys-vault-not-read fix).

// Package backup doc: see doc.go for the canonical package comment.
package backup

import (
	"context"
	"crypto/ed25519"

	"filippo.io/age"

	"github.com/acamarata/cascade/pkg/cascade"
)

// KeySource names which custody backend actually answered a key
// resolution. Exported so a caller with somewhere to report it (a CLI
// result, an audit record) can state the answer explicitly rather than
// treating vault and env-var custody as interchangeable -- the exact
// silent merge that let this defect stand for a day.
type KeySource string

const (
	// KeySourceVault is the S-42.T6 ceremony's vault entry answering.
	KeySourceVault KeySource = "vault"
	// KeySourceEnvFallback is the pre-existing environment variable
	// answering because no vault was available or no vault entry existed.
	// This is a supported, permanent fallback (an operator who has not
	// yet run the ceremony still gets a working backup), but it is always
	// reported as exactly what it is, never merged silently into "vault".
	KeySourceEnvFallback KeySource = "env_fallback"
)

// resolveKeySource decides, without reading any key bytes, whether vault
// or the environment variable will answer for one named key. vault may be
// nil (no elevated broker at this call site); envSet reports whether the
// caller's env var already has a non-empty value (the caller passes this
// in rather than resolveKeySource calling os.Getenv itself, so the exact
// same env read that later decodes the value is the one being reported
// on -- no second, possibly-racing read).
func resolveKeySource(ctx context.Context, vault VaultStore, vaultName string, envSet bool) (KeySource, bool, error) {
	if vault != nil {
		exists, err := vault.Exists(ctx, vaultName)
		if err != nil {
			return "", false, err
		}
		if exists {
			return KeySourceVault, true, nil
		}
	}
	if envSet {
		return KeySourceEnvFallback, true, nil
	}
	return "", false, nil
}

// manifestSigningKeyFromVault reads the ceremony's vault-held Ed25519 seed
// and returns the full private key -- distinct from recovery.go's
// existingManifestSigningPubKey, which deliberately returns only the
// public half for the ceremony's own read-back check. This is the one
// place the private seed is reconstituted for signing.
func manifestSigningKeyFromVault(ctx context.Context, vault VaultStore) (ed25519.PrivateKey, error) {
	raw, err := vault.Get(ctx, ManifestSigningKeyVaultName)
	if err != nil {
		return nil, err
	}
	seed := append([]byte(nil), raw...)
	defer zeroKeyMaterial(seed)
	if len(seed) != ed25519.SeedSize {
		return nil, cascade.Newf(cascade.KindIntegrity,
			"backup: vault-held manifest signing seed is %d bytes, want %d", len(seed), ed25519.SeedSize)
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

// ageIdentityFromVault reads the ceremony's vault-held identity string and
// parses it eagerly, exactly like the env-var path. A value handed back by
// VaultStore.Get is defensively copied before it is zeroed (recovery.go's
// EnsureAgeIdentity documents why: the custody backend may retain the same
// backing array as its own copy).
func ageIdentityFromVault(ctx context.Context, vault VaultStore) (string, error) {
	raw, err := vault.Get(ctx, AgeIdentityVaultName)
	if err != nil {
		return "", err
	}
	cp := append([]byte(nil), raw...)
	defer zeroKeyMaterial(cp)
	identity := string(cp)
	if _, err := age.ParseX25519Identity(identity); err != nil {
		return "", cascade.Wrap(cascade.KindInvalidInput, err, "backup: parse backup age identity")
	}
	return identity, nil
}
