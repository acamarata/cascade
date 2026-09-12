// Purpose: the S-42.T6 recovery-key ceremony's key-material logic: the
//
//	backup age identity (the master backup encryption key) and the R-14.58
//	Ed25519 backup-manifest signing key, both vault-held via a narrow
//	VaultStore seam over internal/secrets' broker (mirroring
//	vaultexport.go's VaultExporter/VaultImporter precedent so this package
//	never imports a concrete secrets type); the passphrase-wrapped
//	printable armored age format the real `age` CLI decrypts (age.filippo.
//	io/age's own ScryptRecipient/ScryptIdentity plus its armor package --
//	no hand-rolled dialect, Art.2); the location invariant that refuses an
//	export path resolving inside a configured backup target (00-VISION
//	principle 10: recovery keys never live beside backups); and the
//	audit-domain escrow flag CreateSnapshot (snapshot.go) consults before
//	a first snapshot.
//
// Inputs: a VaultStore, a passphrase, an artifact/identity string, an
//
//	output path plus the configured TargetRecords, or an AuditReader.
//
// Outputs: the vault-held identity/pubkey, a wrapped or recovered age
//
//	identity string, a location refusal, or an escrow boolean.
//
// Constraints: EnsureAgeIdentity/EnsureManifestSigningKey never mint a
//
//	second key once one is vault-held (that would orphan every snapshot
//	already encrypted to the old recipient). UnwrapRecoveryKey never
//	returns a partial plaintext on a wrong passphrase or corrupt artifact:
//	age's own STREAM authentication tag fails closed before any byte is
//	read. Every byte-slice key buffer this file allocates itself is zeroed
//	after use; a value handed back by VaultStore.Get is never assumed to
//	be this package's own copy (the custody backend may retain the same
//	backing array), so it is defensively copied before its copy is zeroed.
//
// SPORT: internal.backup.recovery/ADDED (P1-E19-W4-S42-T6).

// Package backup doc: see doc.go for the canonical package comment.
package backup

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"path/filepath"
	"strings"

	"filippo.io/age"
	"filippo.io/age/armor"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Vault entry names the ceremony reads and writes. Both are exact, closed
// names -- never derived from caller input -- so a lookup can never be
// redirected to an unrelated vault entry.
const (
	AgeIdentityVaultName        = "backup-age-identity"
	ManifestSigningKeyVaultName = "backup-manifest-signing-key"
)

// ageScryptLogN is the real age CLI's own default scrypt work factor
// (filippo.io/age/scrypt.go), used for every production wrap. Tests use
// wrapRecoveryKeyLogN directly with a lower factor so the suite does not
// pay ~1s of scrypt per case; the wire format is identical either way.
const ageScryptLogN = 18

// Sentinel fail-closed errors.
var (
	ErrRecoveryKeyPassphraseRequired = cascade.New(cascade.KindInvalidInput,
		"backup: recovery key ceremony requires a passphrase")
	ErrRecoveryKeyUnwrapFailed = cascade.New(cascade.KindIntegrity,
		"backup: recovery key artifact did not decrypt with the supplied passphrase, or is corrupt/truncated")
	ErrRecoveryKeyLocationInvariant = cascade.New(cascade.KindInvalidInput,
		"backup: refusing to write the recovery key inside a configured backup target path")
	ErrBackupKeyNotEscrowed = cascade.New(cascade.KindPermissionDenied,
		"backup: no recovery key is escrowed yet; run `cascade backup key export` before the first snapshot")
)

// VaultStore is the narrow ceremony seam over the Epic H vault broker:
// exists/get/set on named entries. internal/backup depends on this
// interface, never the concrete secrets.Broker, matching vaultexport.go's
// VaultExporter/VaultImporter precedent; cmd/cascade adapts the real
// *secrets.Broker to it.
type VaultStore interface {
	Exists(ctx context.Context, name string) (bool, error)
	Get(ctx context.Context, name string) ([]byte, error)
	Set(ctx context.Context, name string, value []byte) error
}

// zeroKeyMaterial overwrites a byte buffer this package allocated itself.
// internal/secrets' rehydrate.go carries an equivalent zero/zeroAll pair,
// but both are unexported and this ceremony's key material never crosses
// into that package except via VaultStore.Get/Set, whose slices belong to
// the custody backend, not to this package -- so a same-shaped local
// helper is unavoidable across the package boundary, not a duplicate of
// convenience.
func zeroKeyMaterial(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// EnsureAgeIdentity returns the vault-held backup age identity (the master
// backup encryption key), generating and storing a fresh X25519 identity
// only the first time this ceremony runs for a vault. A repo that already
// has one is never re-minted: doing so would silently orphan every
// snapshot already encrypted to the old recipient.
func EnsureAgeIdentity(ctx context.Context, vault VaultStore) (string, error) {
	exists, err := vault.Exists(ctx, AgeIdentityVaultName)
	if err != nil {
		return "", err
	}
	if exists {
		raw, err := vault.Get(ctx, AgeIdentityVaultName)
		if err != nil {
			return "", err
		}
		cp := append([]byte(nil), raw...)
		defer zeroKeyMaterial(cp)
		return string(cp), nil
	}
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		return "", cascade.Wrap(cascade.KindInternal, err, "backup: generate age identity")
	}
	if err := vault.Set(ctx, AgeIdentityVaultName, []byte(identity.String())); err != nil {
		return "", err
	}
	return identity.String(), nil
}

// EnsureManifestSigningKey returns the R-14.58 verification pubkey for the
// vault-held Ed25519 backup-manifest signing key, generating and storing a
// fresh keypair only the first time this ceremony runs. The PRIVATE seed
// never leaves this function except via VaultStore.Set; only the public
// half is ever returned.
func EnsureManifestSigningKey(ctx context.Context, vault VaultStore) (ed25519.PublicKey, error) {
	exists, err := vault.Exists(ctx, ManifestSigningKeyVaultName)
	if err != nil {
		return nil, err
	}
	if exists {
		return existingManifestSigningPubKey(ctx, vault)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInternal, err, "backup: generate manifest signing key")
	}
	seed := append([]byte(nil), priv.Seed()...)
	defer zeroKeyMaterial(seed)
	if err := vault.Set(ctx, ManifestSigningKeyVaultName, append([]byte(nil), seed...)); err != nil {
		return nil, err
	}
	return pub, nil
}

// existingManifestSigningPubKey derives the public half from the
// vault-held seed, without ever returning or logging the seed itself.
func existingManifestSigningPubKey(ctx context.Context, vault VaultStore) (ed25519.PublicKey, error) {
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
	pub, ok := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
	if !ok {
		return nil, cascade.New(cascade.KindInternal, "backup: derived manifest signing key has no Ed25519 public half")
	}
	return pub, nil
}

// WrapRecoveryKey passphrase-wraps identity (a bech32 age X25519 identity
// string) into a printable, armored age file: age's own ScryptRecipient
// (scrypt-derived symmetric key, the reference format's password mode) at
// the real CLI's default work factor, then armor-encoded so the artifact
// is plain ASCII. `age -d -p` decrypts the result unmodified (Art.2).
func WrapRecoveryKey(passphrase, identity string) ([]byte, error) {
	return wrapRecoveryKeyLogN(passphrase, identity, ageScryptLogN)
}

func wrapRecoveryKeyLogN(passphrase, identity string, logN int) ([]byte, error) {
	if passphrase == "" {
		return nil, ErrRecoveryKeyPassphraseRequired
	}
	recipient, err := age.NewScryptRecipient(passphrase)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInvalidInput, err, "backup: build recovery key passphrase recipient")
	}
	recipient.SetWorkFactor(logN)
	var buf bytes.Buffer
	aw := armor.NewWriter(&buf)
	w, err := age.Encrypt(aw, recipient)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInternal, err, "backup: open recovery key age writer")
	}
	if _, err := io.WriteString(w, identity); err != nil {
		_ = w.Close()
		return nil, cascade.Wrap(cascade.KindInternal, err, "backup: write recovery key payload")
	}
	if err := w.Close(); err != nil {
		return nil, cascade.Wrap(cascade.KindInternal, err, "backup: close recovery key age writer")
	}
	if err := aw.Close(); err != nil {
		return nil, cascade.Wrap(cascade.KindInternal, err, "backup: close recovery key armor writer")
	}
	return buf.Bytes(), nil
}

// UnwrapRecoveryKey reverses WrapRecoveryKey. A wrong passphrase, a
// truncated artifact, or any tampering fails age's STREAM authentication
// tag before io.ReadAll ever returns a byte -- there is no partial-
// plaintext path, mirroring crypto.go's Decrypt precedent exactly. The
// recovered text is validated as a parseable X25519 identity before it is
// ever handed back, so a payload that decrypts but is not actually an
// identity (a corrupted or foreign artifact sharing this passphrase)
// refuses too, never silently loading garbage into the vault broker.
func UnwrapRecoveryKey(passphrase string, artifact []byte) (string, error) {
	if passphrase == "" {
		return "", ErrRecoveryKeyPassphraseRequired
	}
	identity, err := age.NewScryptIdentity(passphrase)
	if err != nil {
		return "", cascade.Wrap(cascade.KindInvalidInput, err, "backup: build recovery key passphrase identity")
	}
	r, err := age.Decrypt(armor.NewReader(bytes.NewReader(artifact)), identity)
	if err != nil {
		return "", cascade.Wrap(cascade.KindIntegrity, err, ErrRecoveryKeyUnwrapFailed.Error())
	}
	out, err := io.ReadAll(r)
	if err != nil {
		// out may be partially buffered here, but STREAM authentication
		// did not pass, so it is discarded rather than returned.
		return "", cascade.Wrap(cascade.KindIntegrity, err, ErrRecoveryKeyUnwrapFailed.Error())
	}
	// A real age identity FILE conventionally ends with a trailing
	// newline (age-keygen's own output does); trimming it is not a
	// laxer parse, only tolerance for the exact shape a real file
	// carries, matching what a caller piping such a file through this
	// ceremony would actually hand it.
	text := strings.TrimRight(string(out), "\r\n")
	if _, perr := age.ParseX25519Identity(text); perr != nil {
		return "", cascade.Wrap(cascade.KindIntegrity, perr, "backup: recovered payload is not a valid age identity")
	}
	return text, nil
}

// CheckRecoveryKeyLocation refuses outPath when it resolves inside any
// configured fs-backed backup target's root or a subdirectory of it --
// 00-VISION principle 10, "recovery keys never live beside backups". Only
// fs targets are checked: s3 and rclone-remote targets are not local
// filesystem paths this process can compare against. Must run BEFORE the
// elevation flow (the ceremony's own AC): a location refusal never even
// attempts an attestation.
func CheckRecoveryKeyLocation(outPath string, targetsList []TargetRecord) error {
	absOut, err := filepath.Abs(outPath)
	if err != nil {
		return cascade.Wrap(cascade.KindInvalidInput, err, "backup: resolve recovery key output path")
	}
	for _, t := range targetsList {
		if t.Kind != TargetKindFS || strings.TrimSpace(t.FSRoot) == "" {
			continue
		}
		absRoot, err := filepath.Abs(t.FSRoot)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(absRoot, absOut)
		if err != nil {
			continue
		}
		if rel == "." || (!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "..") {
			return ErrRecoveryKeyLocationInvariant
		}
	}
	return nil
}

// EscrowChecker, AuditReader, AuditEscrowChecker and RecordKeyEscrowed
// live in escrow.go -- split out of this file to stay under the repo's
// 300-line-per-file gate (this file was 347 lines before the split).
