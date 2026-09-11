// Purpose: S-41.T2's manifest format (05 §Epic S / refs/chatgpt-2026-08-30
//
//	§21 field set verbatim) and its authenticity mechanism (R-14.58): a
//	dedicated Ed25519 backup-manifest signing key, resolved by vault/env
//	reference only, signs {root_hash, entries}; the blake3 root_hash
//	covers the chunk set. Age AEAD alone is not authenticity (it encrypts
//	TO a public recipient, so any repo writer could produce a validly-
//	decrypting manifest) - the signature is the closed hole.
//
// Inputs: a Manifest built by snapshot.go's CreateSnapshot, or raw stored
//
//	bytes (age ciphertext) for the read/verify path.
//
// Outputs: age-encrypted, Ed25519-signed manifest bytes (Write), or a
//
//	verified Manifest / a typed fail-closed error (Read).
//
// Constraints: written ONCE per snapshot id - WriteManifest refuses
//
//	(KindConflict) if a manifest already exists at that key, never
//	overwriting. Verification fails closed on: invalid signature, AEAD
//	tag failure, root_hash mismatch, unknown encryption version, a
//	broken previous_snapshot chain, or an absent/malformed signing key
//	reference - there is no "verification skipped" path and no flag that
//	disables it. EXPOSURE PROPERTY (explicit, not accidental): Domains
//	(names), ObjectCount, and per-domain object hashes/sizes ARE visible
//	in the decrypted manifest by the source design's own field set
//	(refs/chatgpt-2026-08-30-cascade-health-hub.md §21); a blake3 hash is
//	one-way and never reveals plaintext content, but domain names and
//	object counts are metadata this design deliberately surfaces for
//	restore/verification, not something this ticket hides.
//
// SPORT: internal.backup.manifest/ADD (P1-E19-W4-S41-T2).

// Package backup doc: see doc.go for the canonical package comment.
package backup

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"time"

	"github.com/zeebo/blake3"

	"github.com/acamarata/cascade/pkg/cascade"
)

// manifestEncryptionVersion is the only Encryption.Version this build's
// decoder accepts, mirroring config.go's CurrentLayoutVersion pattern.
const manifestEncryptionVersion = 1

// Sentinel fail-closed errors, one per distinct tamper-detection mode
// (never a single generic "manifest invalid").
var (
	ErrManifestInvalidSignature         = cascade.New(cascade.KindIntegrity, "backup: manifest signature verification failed")
	ErrManifestRootHashMismatch         = cascade.New(cascade.KindIntegrity, "backup: manifest root hash does not match its entries")
	ErrManifestUnknownEncryptionVersion = cascade.New(cascade.KindUnsupported, "backup: manifest encryption version is not supported")
	ErrManifestChainBroken              = cascade.New(cascade.KindIntegrity, "backup: manifest previous_snapshot chain is broken")
)

// ManifestSigningKeyEnvVar is the vault/env reference this ticket resolves
// the Ed25519 backup-manifest signing key from (§D-15 "creds env-ref only"
// - never a literal, never a tracked file). Its value is a base64
// (standard, unpadded-or-padded both accepted) encoding of a 32-byte
// Ed25519 seed. The S-42.T6 ceremony populates the reference; this ticket
// never generates a key and carries no dependency on that later ticket -
// when the reference is absent, ManifestSigningKey refuses.
const ManifestSigningKeyEnvVar = "CASCADE_BACKUP_MANIFEST_SIGNING_KEY"

// ErrManifestSigningKeyMissing is CreateSnapshot's fail-closed refusal
// when ManifestSigningKeyEnvVar is unset: the S-42.T6 "REQUIRED before
// first snapshot" rule enforced at the engine. A snapshot is never taken
// unencrypted or unsigned.
var ErrManifestSigningKeyMissing = cascade.New(cascade.KindInvalidInput,
	"backup: "+ManifestSigningKeyEnvVar+" is not set; run the recovery-key ceremony before the first snapshot")

// ManifestSigningKey resolves the Ed25519 signing key from
// ManifestSigningKeyEnvVar. It never falls back to a literal or generates
// one; an absent or malformed reference is refused, never a "verification
// skipped" default.
func ManifestSigningKey() (ed25519.PrivateKey, error) {
	raw := os.Getenv(ManifestSigningKeyEnvVar)
	if raw == "" {
		return nil, ErrManifestSigningKeyMissing
	}
	seed, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInvalidInput, err, "backup: decode manifest signing key seed")
	}
	if len(seed) != ed25519.SeedSize {
		return nil, cascade.Newf(cascade.KindInvalidInput,
			"backup: manifest signing key seed is %d bytes, want %d", len(seed), ed25519.SeedSize)
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

// EncryptionInfo is the §21 encryption sub-object.
type EncryptionInfo struct {
	Version   int    `json:"version"`
	Algorithm string `json:"algorithm"`
}

// ManifestObjectRef is one stored chunk's identity within a manifest entry
// - ObjectRef (pipeline.go) with its hash hex-encoded for a JSON-friendly,
// human-diffable manifest document.
type ManifestObjectRef struct {
	Hash string `json:"hash"`
	Size int    `json:"size"`
}

// ManifestEntry is one captured domain's ordered object list.
type ManifestEntry struct {
	Domain string              `json:"domain"`
	Refs   []ManifestObjectRef `json:"refs"`
}

// Manifest is the §21 field set verbatim, plus the R-14.58 signature.
// Written ONCE per Snapshot id; a correction or a later run produces a NEW
// snapshot, never a mutation of an existing manifest.
type Manifest struct {
	Snapshot         SnapshotID      `json:"snapshot"`
	Created          time.Time       `json:"created"`
	Domains          []string        `json:"domains"`
	ObjectCount      int             `json:"objects"`
	RootHash         string          `json:"root_hash"`
	PreviousSnapshot SnapshotID      `json:"previous_snapshot,omitempty"`
	Encryption       EncryptionInfo  `json:"encryption"`
	Entries          []ManifestEntry `json:"entries"`
	SignatureAlgo    string          `json:"signature_algorithm"`
	Signature        string          `json:"signature"`
}

// signedPayload is the R-14.58 AC's literal signing scope: "signed over
// {root_hash, entry list}" - not the whole document (Created/Domains are
// redundant with Entries and this keeps the signed scope minimal and
// exactly what the AC names).
type signedPayload struct {
	RootHash string          `json:"root_hash"`
	Entries  []ManifestEntry `json:"entries"`
}

func (m Manifest) signedBytes() ([]byte, error) {
	data, err := json.Marshal(signedPayload{RootHash: m.RootHash, Entries: m.Entries})
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInternal, err, "backup: encode manifest signing payload")
	}
	return data, nil
}

// computeRootHash is the blake3 root hash over the chunk set: for every
// entry (already domain-sorted by the caller) and every ref in stream
// order, the raw hash bytes then an 8-byte big-endian size - any mutation
// of any chunk's identity or size, or the domain/entry order, changes it.
func computeRootHash(entries []ManifestEntry) (string, error) {
	h := blake3.New()
	for _, e := range entries {
		_, _ = h.Write([]byte(e.Domain))
		for _, r := range e.Refs {
			raw, err := hex.DecodeString(r.Hash)
			if err != nil {
				return "", cascade.Wrap(cascade.KindInvalidInput, err, "backup: decode ref hash for root hash")
			}
			_, _ = h.Write(raw)
			var size [8]byte
			binary.BigEndian.PutUint64(size[:], uint64(r.Size))
			_, _ = h.Write(size[:])
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// SignManifest computes m's RootHash from its Entries and signs
// {root_hash, entries} with signingKey, setting Signature/SignatureAlgo.
// Called exactly once per snapshot, by CreateSnapshot, before the
// manifest is ever written - a signed manifest's RootHash and Entries
// never change afterward.
func SignManifest(m *Manifest, signingKey ed25519.PrivateKey) error {
	if len(signingKey) != ed25519.PrivateKeySize {
		return cascade.New(cascade.KindInvalidInput, "backup: SignManifest requires a valid Ed25519 private key")
	}
	root, err := computeRootHash(m.Entries)
	if err != nil {
		return err
	}
	m.RootHash = root
	m.ObjectCount = countManifestObjects(m.Entries)
	payload, err := m.signedBytes()
	if err != nil {
		return err
	}
	m.SignatureAlgo = "ed25519"
	m.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(signingKey, payload))
	return nil
}

func countManifestObjects(entries []ManifestEntry) int {
	n := 0
	for _, e := range entries {
		n += len(e.Refs)
	}
	return n
}

// DecodeManifestUnverified parses data (already age-decrypted plaintext)
// as a Manifest, schema-validating only - NOT signature or root-hash
// verification (VerifyManifest is separate so FuzzSnapshotManifestDecode
// can exercise the parser alone, mirroring config.go's DecodeRepoConfig
// precedent). Never panics on any input.
func DecodeManifestUnverified(data []byte) (Manifest, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return Manifest{}, cascade.Wrap(cascade.KindInvalidInput, err, "backup: malformed manifest")
	}
	if dec.More() {
		return Manifest{}, cascade.New(cascade.KindInvalidInput, "backup: manifest has trailing data")
	}
	if err := validateManifestSchema(m); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

func validateManifestSchema(m Manifest) error {
	if m.Snapshot == "" {
		return cascade.New(cascade.KindInvalidInput, "backup: manifest missing snapshot id")
	}
	if m.Encryption.Version != manifestEncryptionVersion {
		return cascade.Wrapf(cascade.KindUnsupported, ErrManifestUnknownEncryptionVersion, "got version %d", m.Encryption.Version)
	}
	if m.SignatureAlgo != "ed25519" {
		return cascade.Newf(cascade.KindUnsupported, "backup: manifest signature algorithm %q is not supported", m.SignatureAlgo)
	}
	if m.Signature == "" {
		return cascade.New(cascade.KindInvalidInput, "backup: manifest has no signature")
	}
	return nil
}

// VerifyManifest fail-closed-verifies m: root-hash recomputation, Ed25519
// signature against pubKey, and (when previous is non-nil) the
// previous_snapshot chain. There is no partial-trust return: every check
// failure is its own typed sentinel, never a best-effort acceptance.
func VerifyManifest(m Manifest, pubKey ed25519.PublicKey, previous *Manifest) error {
	if len(pubKey) != ed25519.PublicKeySize {
		return cascade.New(cascade.KindInvalidInput, "backup: VerifyManifest requires a valid Ed25519 public key")
	}
	wantRoot, err := computeRootHash(m.Entries)
	if err != nil {
		return err
	}
	if wantRoot != m.RootHash {
		return ErrManifestRootHashMismatch
	}
	payload, err := m.signedBytes()
	if err != nil {
		return err
	}
	sig, err := base64.StdEncoding.DecodeString(m.Signature)
	if err != nil {
		return cascade.Wrap(cascade.KindIntegrity, err, "backup: decode manifest signature")
	}
	if !ed25519.Verify(pubKey, payload, sig) {
		return ErrManifestInvalidSignature
	}
	return verifyManifestChain(m, previous)
}

// verifyManifestChain enforces the §21 versioning/rollback property: the
// first snapshot has no predecessor (PreviousSnapshot must be empty with a
// nil previous); every later one must name exactly previous.Snapshot.
func verifyManifestChain(m Manifest, previous *Manifest) error {
	if previous == nil {
		if m.PreviousSnapshot != "" {
			return ErrManifestChainBroken
		}
		return nil
	}
	if m.PreviousSnapshot != previous.Snapshot {
		return ErrManifestChainBroken
	}
	return nil
}
