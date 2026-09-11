// Purpose: the fail-closed integrity gate (05 §Epic S S-41.T4) — the
//
//	read-only verification pass `backup verify` (S-42.T3) and S-42.4's
//	verification cron consume, and the one entry point restore.go's
//	Restore calls FIRST, before touching a single domain. It walks
//	Manifest's previous_snapshot chain back to genesis re-verifying every
//	ancestor's signature/root_hash/link (S-41.T2's manifest.go), then
//	confirms the TARGET snapshot's own object set — and only the target
//	snapshot's — is complete and authentic by re-decrypting every stored
//	chunk it lists (pipeline.go's readOneChunk — reused as-is, so this
//	file adds no new parser per 06 §5.7).
//
// Scope decision (deliberate, not an oversight — see VerifyIntegrity's own
// doc comment for the full statement and TestIntegrityGate_ObjectScopeIs-
// TargetSnapshotOnly for the pinning test): object-level re-decryption
// covers the id argument's own manifest entries only. An ancestor's
// MANIFEST is cryptographically re-verified (signature, root_hash, link)
// by every chain walk; an ancestor's OBJECTS — chunks referenced only by
// an older snapshot and not by the target — are not read. This keeps
// "can I restore this snapshot" (Restore's actual question, and the one
// a corrupted, no-longer-needed ancient chunk must never block) separate
// from "is the entire backup history's every chunk still intact" (a
// distinct, chain-wide question a future full-history audit would answer
// by calling VerifyIntegrity once per snapshot id, at O(total chunks in
// the whole chain) cost, not by this single call silently doing that work
// under one id).
//
// Inputs: a Target, the S-42.T6 backup age identity (resolved here, via
//
//	vault/env-ref only — never caller-supplied literal), the manifest
//	verification pubkey, and the snapshot id to verify.
//
// Outputs: the verified target Manifest plus a GateReport, or a typed
//
//	fail-closed error.
//
// Constraints: every failure mode is a distinct taxonomy error; there is
//
//	no partial-trust return and no "verification skipped" path. A
//	chain that revisits a snapshot id (a corrupted/malicious
//	previous_snapshot pointer) refuses rather than looping forever.
//
// SPORT: internal.backup.integrity/ADDED (P1-E19-W4-S41-T4).

package backup

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"io"
	"os"

	"filippo.io/age"

	"github.com/acamarata/cascade/pkg/cascade"
)

// AgeIdentityEnvVar is the vault/env reference the integrity gate and
// restore both resolve the backup age IDENTITY (private decrypt key) from
// (§D-15 "creds env-ref only"; 00-VISION principle 10: recovery keys never
// live beside backups, so this value is never read from anywhere in the
// repo/target itself). The S-42.T6 recovery-key ceremony populates it;
// this ticket carries no import of that later ticket's code, mirroring
// manifest.go's ManifestSigningKeyEnvVar precedent.
const AgeIdentityEnvVar = "CASCADE_BACKUP_AGE_IDENTITY"

// ErrAgeIdentityMissing is the fail-closed refusal when
// AgeIdentityEnvVar is unset: neither verify nor restore ever runs
// with no key material.
var ErrAgeIdentityMissing = cascade.New(cascade.KindInvalidInput,
	"backup: "+AgeIdentityEnvVar+" is not set; run the recovery-key ceremony before verify or restore")

// ErrGateChainCycle reports a previous_snapshot chain that revisits a
// snapshot id — a corrupted or adversarial manifest, never a legitimate
// history (snapshot ids are minted fresh, never reused).
var ErrGateChainCycle = cascade.New(cascade.KindIntegrity,
	"backup: manifest previous_snapshot chain revisits a snapshot id")

// ErrGateObjectHashMalformed reports a manifest entry whose hex-encoded
// object hash cannot be decoded — a malformed manifest document that
// schema validation's string type alone does not catch.
var ErrGateObjectHashMalformed = cascade.New(cascade.KindIntegrity,
	"backup: manifest object ref hash is not valid hex")

// AgeIdentity resolves the backup age identity from
// AgeIdentityEnvVar, parsing it eagerly (via the reference age
// library) so a malformed reference fails closed here rather than at the
// first Decrypt call several steps later.
func AgeIdentity() (string, error) {
	raw := os.Getenv(AgeIdentityEnvVar)
	if raw == "" {
		return "", ErrAgeIdentityMissing
	}
	if _, err := age.ParseX25519Identity(raw); err != nil {
		return "", cascade.Wrap(cascade.KindInvalidInput, err, "backup: parse backup age identity")
	}
	return raw, nil
}

// GateOptions collects VerifyIntegrity's collaborators.
type GateOptions struct {
	Target Target
	PubKey ed25519.PublicKey
}

// GateReport summarizes one passed VerifyIntegrity call. The two counters
// measure DIFFERENT scopes, deliberately (see VerifyIntegrity's doc
// comment): ChainDepth is chain-wide (every ancestor manifest reached by
// the walk), ObjectsVerified is NOT (id's own manifest entries only) — a
// large ChainDepth next to a small ObjectsVerified is expected on a long
// history and is not itself a sign anything was skipped that should not
// have been.
type GateReport struct {
	Snapshot   SnapshotID
	ChainDepth int // ancestor MANIFESTS walked and re-verified, including id itself
	// ObjectsVerified counts objects re-decrypted and hash-checked from
	// id's OWN manifest entries only — never an ancestor's. A chunk
	// referenced solely by an older snapshot is not counted here and is
	// not read by this call at all.
	ObjectsVerified int
}

// VerifyIntegrity is the fail-closed gate: resolve the backup age identity,
// walk id's previous_snapshot chain back to genesis re-verifying every
// ancestor MANIFEST (signature, root_hash, link), then re-decrypt and
// hash-check every object id's OWN manifest lists. A damaged repo at any
// of these steps never passes: the first failure is returned immediately.
//
// Object-verification scope, stated plainly because "integrity verified"
// that silently means only "the tip is restorable" is a false assurance:
// this call reads and authenticates the chunks id's manifest references.
// It does NOT read a chunk that exists solely because an OLDER ancestor
// snapshot in the same chain wrote it and id's own manifest never refers
// back to it. Ancestor manifests are still fully cryptographically
// verified (their signature, root_hash, and previous_snapshot link), so a
// tampered or forged ancestor is caught; a bit-rotted ancestor OBJECT that
// no living manifest still needs is not. To verify an ancestor snapshot's
// own objects, call VerifyIntegrity with that ancestor's SnapshotID
// directly — each call's ObjectsVerified always describes only the id it
// was given.
func VerifyIntegrity(ctx context.Context, opts GateOptions, id SnapshotID) (Manifest, GateReport, error) {
	if len(opts.PubKey) != ed25519.PublicKeySize {
		return Manifest{}, GateReport{}, cascade.New(cascade.KindInvalidInput,
			"backup: VerifyIntegrity requires a valid Ed25519 public key")
	}
	identity, err := AgeIdentity()
	if err != nil {
		return Manifest{}, GateReport{}, err
	}
	ids, err := chaseChainIDs(ctx, opts.Target, identity, id)
	if err != nil {
		return Manifest{}, GateReport{}, err
	}
	target, err := verifyChainForward(ctx, opts.Target, identity, opts.PubKey, ids)
	if err != nil {
		return Manifest{}, GateReport{}, err
	}
	verified, err := verifyObjectCompleteness(ctx, opts.Target, identity, target)
	if err != nil {
		return Manifest{}, GateReport{}, err
	}
	return target, GateReport{Snapshot: target.Snapshot, ChainDepth: len(ids), ObjectsVerified: verified}, nil
}

// chaseChainIDs walks id's previous_snapshot pointers back to genesis,
// schema-decoding each manifest only (DecodeManifestUnverified — no
// signature/root_hash check yet: cryptographic verification is
// verifyChainForward's job, once every ancestor id is known in order).
// Returned oldest-first. A chain that revisits an id refuses
// (ErrGateChainCycle) rather than looping forever.
func chaseChainIDs(ctx context.Context, t Target, identityStr string, id SnapshotID) ([]SnapshotID, error) {
	visited := map[SnapshotID]bool{}
	var idsRev []SnapshotID
	cur := id
	for {
		if visited[cur] {
			return nil, ErrGateChainCycle
		}
		visited[cur] = true
		idsRev = append(idsRev, cur)
		m, err := fetchManifestUnverified(ctx, t, identityStr, cur)
		if err != nil {
			return nil, err
		}
		if m.PreviousSnapshot == "" {
			break
		}
		cur = m.PreviousSnapshot
	}
	ids := make([]SnapshotID, len(idsRev))
	for i, v := range idsRev {
		ids[len(idsRev)-1-i] = v
	}
	return ids, nil
}

// verifyChainForward re-fetches and fully verifies ids (oldest-first) via
// manifest.go's ReadManifest — fetch, age-decrypt, schema-decode, and
// VerifyManifest (signature, root_hash, previous_snapshot link) in one
// call per ancestor, each one's real predecessor supplied from this same
// walk. This is the gate's one call onto ReadManifest (S-41.T2's
// documented "S-41.T4's restore integrity gate" caller): a tampered or
// wrongly-signed ancestor, a root_hash mismatch anywhere in the history,
// or a broken link fails here, not only a check against the target
// snapshot in isolation. Returns the newest (target) manifest.
func verifyChainForward(ctx context.Context, t Target, identityStr string, pubKey ed25519.PublicKey, ids []SnapshotID) (Manifest, error) {
	var prev *Manifest
	for _, id := range ids {
		m, err := ReadManifest(ctx, t, identityStr, id, pubKey, prev)
		if err != nil {
			return Manifest{}, err
		}
		verified := m
		prev = &verified
	}
	return *prev, nil
}

// fetchManifestUnverified fetches and age-decrypts the manifest stored at
// id, schema-decoding it (no signature/root_hash check yet — see
// loadManifestChain). Reuses manifest.go's manifestKey and
// DecodeManifestUnverified and crypto.go's Decrypt: no new parser.
func fetchManifestUnverified(ctx context.Context, t Target, identityStr string, id SnapshotID) (Manifest, error) {
	rc, err := t.Get(ctx, manifestKey(id))
	if err != nil {
		return Manifest{}, err
	}
	defer func() { _ = rc.Close() }()
	ciphertext, err := io.ReadAll(rc)
	if err != nil {
		return Manifest{}, cascade.Wrap(cascade.KindUnavailable, err, "backup: read manifest body")
	}
	plaintext, err := Decrypt(identityStr, ciphertext)
	if err != nil {
		return Manifest{}, err
	}
	return DecodeManifestUnverified(plaintext)
}

// verifyObjectCompleteness re-decrypts and hash-checks every object m's
// entries list, via pipeline.go's readOneChunk (the pipeline's own
// verification half — AEAD tag authentication then a recovered-plaintext
// hash check), reused rather than re-implemented: this is simultaneously
// the "object presence" check (a missing object surfaces readOneChunk's
// KindNotFound from GetObject) and the "AEAD tag verification on every
// object read" check the ticket names — one real decrypt proves both.
func verifyObjectCompleteness(ctx context.Context, t Target, identityStr string, m Manifest) (int, error) {
	verified := 0
	for _, entry := range m.Entries {
		for _, ref := range entry.Refs {
			objRef, err := manifestRefToObjectRef(ref)
			if err != nil {
				return verified, err
			}
			if _, err := readOneChunk(ctx, t, identityStr, objRef); err != nil {
				return verified, err
			}
			verified++
		}
	}
	return verified, nil
}

// manifestRefToObjectRef is the inverse of snapshot.go's toManifestRefs:
// decode a manifest's hex-encoded hash back to pipeline.go's [32]byte
// shape.
func manifestRefToObjectRef(ref ManifestObjectRef) (ObjectRef, error) {
	raw, err := hex.DecodeString(ref.Hash)
	if err != nil || len(raw) != 32 {
		return ObjectRef{}, ErrGateObjectHashMalformed
	}
	var hash [32]byte
	copy(hash[:], raw)
	return ObjectRef{Hash: hash, Size: ref.Size}, nil
}
