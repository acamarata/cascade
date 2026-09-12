// Purpose: S-41.T2's snapshot record and the elevated snapshot-create flow
//
//	that orchestrates S-41.T1's capture adapters + pipeline over a
//	supplied domain set, writing the resulting signed manifest. This is
//	Snapshot's (pipeline.go) real production caller - see this file's
//	CreateSnapshot - which retires that symbol's
//	internal/build/testonly-allow.json exemption.
//
// Inputs: an ElevationProof, a CreateSnapshotDeps naming the target, age
//
//	recipient, injected clock, and one Exporter per domain name.
//
// Outputs: the signed, written Manifest for the new snapshot.
// Constraints: `backup create` is 06 §5.14's elevated verb - CreateSnapshot
//
//	refuses (KindElevationRequired) without a non-empty proof; the real
//	CLI/MCP attestation flow that produces one is S-42.T3's, out of this
//	ticket's scope (this is the operation boundary, not the gate). The
//	fail-closed key rule is enforced here too: no signing key reference,
//	no manifest, no snapshot - ever.
//
// SPORT: internal.backup.snapshot/ADD (P1-E19-W4-S41-T2).

// Package backup doc: see doc.go for the canonical package comment.
package backup

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"sort"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// ElevationProof is the minimal boundary evidence CreateSnapshot requires
// (06 §5.14 `backup create` ⚠): a non-empty caller-supplied proof, opaque
// to this package. S-42.T3 owns the CLI/MCP attestation flow that
// produces a real one; this operation only enforces its presence at the
// operation boundary, never verifies its content itself.
type ElevationProof string

// ErrElevationRequired is CreateSnapshot's refusal for a missing proof.
var ErrElevationRequired = cascade.New(cascade.KindElevationRequired,
	"backup: snapshot create requires a valid elevation attestation")

// SnapshotID names one snapshot (ULID-shaped, minted via cascade.NewID()).
type SnapshotID string

// SnapshotRecord is the §21 snapshot identity: id, creation time (from the
// injected clock, never bare time.Now), the captured domain set, and
// previous_snapshot chaining - the first snapshot has no predecessor.
type SnapshotRecord struct {
	ID               SnapshotID
	Created          time.Time
	Domains          []string
	PreviousSnapshot SnapshotID
}

// CreateSnapshotDeps collects CreateSnapshot's constructor-time
// collaborators. Domains maps a domain name to the capture adapter that
// exports it (S-41.T1's SQLiteCapture, or any future Exporter). Escrow is
// the S-42.T6 recovery-key escrow guard: OPTIONAL (nil skips the check,
// preserving every caller that predates the ceremony), but the production
// composition root (cmd/cascade) always supplies a real
// AuditEscrowChecker, so the shipped `backup create` and scheduled-fire
// paths are fail-closed in practice even though the engine itself
// tolerates an unconfigured caller.
type CreateSnapshotDeps struct {
	Target       Target
	AgeRecipient string
	Clock        runtime.Clock
	Domains      map[string]Exporter
	Escrow       EscrowChecker
}

// CreateSnapshot runs the elevated snapshot-create operation: for every
// named domain, capture (S-41.T1's adapters) -> pipeline
// (chunk/dedup/zstd/encrypt, only new/changed chunks stored) -> collect
// object refs, then sign and write one immutable manifest chained to
// previous (nil for the very first snapshot). No dispatch happens before
// the elevation and signing-key checks both pass.
func CreateSnapshot(ctx context.Context, proof ElevationProof, deps CreateSnapshotDeps, previous *Manifest) (Manifest, error) {
	if proof == "" {
		return Manifest{}, ErrElevationRequired
	}
	signingKey, err := ManifestSigningKey()
	if err != nil {
		return Manifest{}, err
	}
	if deps.Clock == nil {
		return Manifest{}, cascade.New(cascade.KindInvalidInput, "backup: CreateSnapshot requires a non-nil clock")
	}
	if len(deps.Domains) == 0 {
		return Manifest{}, cascade.New(cascade.KindInvalidInput, "backup: CreateSnapshot requires at least one domain")
	}
	if err := checkEscrowed(ctx, deps.Escrow); err != nil {
		return Manifest{}, err
	}
	entries, names, err := captureAllDomains(ctx, deps)
	if err != nil {
		return Manifest{}, err
	}
	pubKey, ok := signingKey.Public().(ed25519.PublicKey)
	if !ok {
		return Manifest{}, cascade.New(cascade.KindInternal, "backup: resolved signing key has no Ed25519 public half")
	}
	if err := ensureManifestSigningPubKey(ctx, deps.Target, pubKey); err != nil {
		return Manifest{}, err
	}
	id, err := newSnapshotID()
	if err != nil {
		return Manifest{}, err
	}
	rec := SnapshotRecord{ID: id, Created: deps.Clock.Now(), Domains: names}
	if previous != nil {
		rec.PreviousSnapshot = previous.Snapshot
	}
	m := manifestFromRecord(rec, entries)
	if err := SignManifest(&m, signingKey); err != nil {
		return Manifest{}, err
	}
	if err := WriteManifest(ctx, deps.Target, deps.AgeRecipient, m); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

// checkEscrowed enforces the S-42.T6 escrow guard when one is configured.
// A nil checker is a documented no-op (see CreateSnapshotDeps.Escrow); a
// configured checker that reports false is ErrBackupKeyNotEscrowed, the
// same refusal for a scheduled fire and a manual create alike, since both
// paths call this same function with no separate branch of their own.
func checkEscrowed(ctx context.Context, checker EscrowChecker) error {
	if checker == nil {
		return nil
	}
	escrowed, err := checker.Escrowed(ctx)
	if err != nil {
		return err
	}
	if !escrowed {
		return ErrBackupKeyNotEscrowed
	}
	return nil
}

// captureAllDomains runs Snapshot (pipeline.go) once per domain, in
// sorted-name order (deterministic manifest entry order, and therefore a
// deterministic root hash for identical input regardless of map
// iteration), and collects each domain's object refs into a ManifestEntry.
func captureAllDomains(ctx context.Context, deps CreateSnapshotDeps) ([]ManifestEntry, []string, error) {
	names := make([]string, 0, len(deps.Domains))
	for name := range deps.Domains {
		names = append(names, name)
	}
	sort.Strings(names)

	entries := make([]ManifestEntry, 0, len(names))
	for _, name := range names {
		_, refs, err := Snapshot(ctx, deps.Target, deps.AgeRecipient, deps.Domains[name])
		if err != nil {
			return nil, nil, cascade.Wrapf(cascade.KindUnavailable, err, "backup: capture domain %q", name)
		}
		entries = append(entries, ManifestEntry{Domain: name, Refs: toManifestRefs(refs)})
	}
	return entries, names, nil
}

// toManifestRefs projects pipeline.go's []ObjectRef onto the manifest's
// JSON-friendly, hex-encoded shape.
func toManifestRefs(refs []ObjectRef) []ManifestObjectRef {
	out := make([]ManifestObjectRef, len(refs))
	for i, r := range refs {
		out[i] = ManifestObjectRef{Hash: hex.EncodeToString(r.Hash[:]), Size: r.Size}
	}
	return out
}

// ensureManifestSigningPubKey persists pubKey (the verification half of
// the vault-resolved signing key) into the repo's config document on
// first use, and refuses (KindConflict) if a DIFFERENT pubkey is already
// bound - mirroring pipeline.go's ensureRepoConfig recipient-mismatch
// precedent: this repo's manifest verification key never silently
// rotates. Called after captureAllDomains so Snapshot's own
// ensureRepoConfig has already created the document.
func ensureManifestSigningPubKey(ctx context.Context, t Target, pubKey ed25519.PublicKey) error {
	cfg, err := ReadRepoConfig(ctx, t)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "backup: read repo config for manifest signing pubkey")
	}
	encoded := base64.StdEncoding.EncodeToString(pubKey)
	if cfg.ManifestSigningPubKey == "" {
		cfg.ManifestSigningPubKey = encoded
		return WriteRepoConfig(ctx, t, cfg)
	}
	if cfg.ManifestSigningPubKey != encoded {
		return cascade.New(cascade.KindConflict, "backup: repo is already bound to a different manifest signing key")
	}
	return nil
}

// manifestFromRecord projects rec plus its captured entries onto the
// unsigned Manifest shape - the record and the manifest are deliberately
// distinct: rec is this ticket's own §21 snapshot identity, and the
// manifest is the immutable, signed artifact derived from it once per
// snapshot (SignManifest fills RootHash/ObjectCount/Signature after this).
func manifestFromRecord(rec SnapshotRecord, entries []ManifestEntry) Manifest {
	return Manifest{
		Snapshot:         rec.ID,
		Created:          rec.Created,
		Domains:          rec.Domains,
		Entries:          entries,
		PreviousSnapshot: rec.PreviousSnapshot,
		Encryption:       EncryptionInfo{Version: manifestEncryptionVersion, Algorithm: "age-x25519+zstd"},
	}
}

// newSnapshotID mints a fresh ULID-shaped SnapshotID via cascade.NewID().
func newSnapshotID() (SnapshotID, error) {
	id, err := cascade.NewID()
	if err != nil {
		return "", cascade.Wrap(cascade.KindInternal, err, "backup: mint snapshot id")
	}
	return SnapshotID(id), nil
}

// manifestKey returns the manifests/ layout key for id.
func manifestKey(id SnapshotID) string {
	return repoManifestsDir + "/" + string(id) + ".json"
}

// WriteManifest age-encrypts m (already signed by SignManifest) to
// recipient and stores it under manifests/, refusing (KindConflict) if a
// manifest already exists at that key - immutability: written ONCE, never
// overwritten; corrections are new snapshots.
func WriteManifest(ctx context.Context, t Target, recipient string, m Manifest) error {
	key := manifestKey(m.Snapshot)
	if _, err := t.Get(ctx, key); err == nil {
		return cascade.Newf(cascade.KindConflict, "backup: manifest %s already exists; snapshots are immutable", m.Snapshot)
	} else if !cascade.HasKind(err, cascade.KindNotFound) {
		return cascade.Wrap(cascade.KindUnavailable, err, "backup: probe existing manifest")
	}
	data, err := json.Marshal(m)
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "backup: encode manifest")
	}
	encrypted, err := Encrypt(recipient, data)
	if err != nil {
		return err
	}
	if err := t.Put(ctx, key, bytes.NewReader(encrypted)); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "backup: write manifest")
	}
	return nil
}

// ReadManifest fetches, age-decrypts, schema-validates and fully verifies
// the manifest at id - the one path S-41.T4's restore and S-42.T4's
// verification cron consume. Every failure mode is typed and fail-closed;
// there is no partial-trust return.
func ReadManifest(ctx context.Context, t Target, identityStr string, id SnapshotID, pubKey ed25519.PublicKey, previous *Manifest) (Manifest, error) {
	rc, err := t.Get(ctx, manifestKey(id))
	if err != nil {
		if cascade.HasKind(err, cascade.KindNotFound) {
			return Manifest{}, err
		}
		return Manifest{}, cascade.Wrap(cascade.KindUnavailable, err, "backup: read manifest")
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
	m, err := DecodeManifestUnverified(plaintext)
	if err != nil {
		return Manifest{}, err
	}
	if err := VerifyManifest(m, pubKey, previous); err != nil {
		return Manifest{}, err
	}
	return m, nil
}
