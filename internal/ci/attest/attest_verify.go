package attest

// Purpose: TrustStore, VerifySignature, DecodeCanonical and the attestation
// error sentinels (contract:ci-attestation).
//
// Inputs: a SignedAttestation, the commit and tree it must bind (the caller
// resolves the tree from the commit itself), a TrustStore and the
// verifier's clock reading.
// Outputs: nil only for a record whose canonical bytes verify under the
// node's current key, are unexpired, and bind exactly that commit and tree.
// Constraints: the record is decoded ONLY from sa.CanonicalJSON (sa.Record
// is ignored); verification never consumes a nonce. errors.Is on a cascade
// error compares Kind only; each sentinel carries its own Kind, and callers
// that need the exact sentinel compare identity on the Unwrap chain.
// SPORT: internal.ci.attest.TrustStore/ADDED,
// internal.ci.attest.VerifySignature/ADDED (P1-CI-04).

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"slices"
	"time"

	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/pkg/cascade"
)

// MaxAttestationLifetime caps expires_at at 24 h after started_at.
const MaxAttestationLifetime = 24 * time.Hour

// maxClockSkew is how far a signer's started_at may run ahead of the
// verifier's clock; a record starting later is not yet valid, so a
// future-dated record cannot stretch the 24 h window.
const maxClockSkew = 5 * time.Minute

// Attestation error sentinels, one cascade Kind each.
var (
	// ErrUnknownNode: no trust store knows the attesting node.
	ErrUnknownNode = cascade.New(cascade.KindNotFound, "attest: the attesting node is not enrolled")
	// ErrInvalidSignature: the signature does not verify.
	ErrInvalidSignature = cascade.New(cascade.KindIntegrity, "attest: the attestation signature does not verify")
	// ErrExpiredAttestation: the record or its nonce has expired, or the
	// record claims a lifetime above 24 h.
	ErrExpiredAttestation = cascade.New(cascade.KindPolicyDenied, "attest: the attestation has expired")
	// ErrInvalidRecord: malformed, non-canonical or mis-bound record bytes.
	ErrInvalidRecord = cascade.New(cascade.KindInvalidInput, "attest: the attestation record is invalid")
	// ErrRevokedKey: the key id is not the node's current, unrevoked key.
	ErrRevokedKey = cascade.New(cascade.KindPermissionDenied, "attest: the signing key is revoked or superseded")
	// ErrNonceReused: the attestation nonce was already consumed.
	ErrNonceReused = cascade.New(cascade.KindConflict, "attest: the attestation nonce was already used")
	// ErrUnverifiedAttestor: the derived attestor class is not eligible.
	ErrUnverifiedAttestor = cascade.New(cascade.KindCapabilityDenied, "attest: the attestor class is not eligible")
)

// TrustStore resolves the public key a node signed with.
type TrustStore interface {
	// Lookup returns nodeID's public key for keyID: ErrUnknownNode for an
	// unknown node, ErrRevokedKey for any key id other than the current.
	Lookup(ctx context.Context, nodeID, keyID string) (ed25519.PublicKey, error)
}

// VerifySignature verifies sa against commitSHA and treeHash: canonical
// bytes only, the current key of (node_id, key_id) through ts, Ed25519 over
// the domain-separated bytes, unexpired at now with a lifetime of at most
// 24 h, and both commit_sha and tree_hash equal to the caller's values.
func VerifySignature(ctx context.Context, sa SignedAttestation, commitSHA, treeHash string, ts TrustStore, now time.Time) error {
	if ts == nil {
		return cascade.New(cascade.KindInvalidInput, "attest: VerifySignature requires a trust store")
	}
	rec, err := verifiedRecord(ctx, sa, ts)
	if err != nil {
		return err
	}
	if !now.Before(rec.ExpiresAt) || rec.ExpiresAt.Sub(rec.StartedAt) > MaxAttestationLifetime ||
		rec.StartedAt.After(now.Add(maxClockSkew)) || rec.EndedAt.Before(rec.StartedAt) || rec.ExpiresAt.Before(rec.EndedAt) {
		return cascade.Wrap(cascade.KindPolicyDenied, ErrExpiredAttestation, "attest: record outside its validity window (expired, over 24 h, not yet started, or out of order)")
	}
	if commitSHA == "" || treeHash == "" || rec.CommitSHA != commitSHA || rec.TreeHash != treeHash {
		return invalidRecord("the record does not bind this commit and tree")
	}
	return nil
}

// verifiedRecord decodes sa.CanonicalJSON and checks its signature under
// the key ts resolves for the record's own (node_id, key_id).
func verifiedRecord(ctx context.Context, sa SignedAttestation, ts TrustStore) (AttestationRecord, error) {
	rec, err := DecodeCanonical(sa.CanonicalJSON)
	if err != nil {
		return AttestationRecord{}, err
	}
	sig, err := base64.RawStdEncoding.DecodeString(sa.Signature)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return AttestationRecord{}, cascade.Wrap(cascade.KindIntegrity, ErrInvalidSignature, "attest: signature is not base64 Ed25519")
	}
	pub, err := ts.Lookup(ctx, rec.NodeID, rec.KeyID)
	if err != nil {
		return AttestationRecord{}, err
	}
	if len(pub) != ed25519.PublicKeySize || nodes.KeyIDFor(pub) != rec.KeyID {
		return AttestationRecord{}, cascade.Wrap(cascade.KindPermissionDenied, ErrRevokedKey, "attest: the trust store answered with a key other than key_id")
	}
	if !ed25519.Verify(pub, signingPayload(sa.CanonicalJSON), sig) {
		return AttestationRecord{}, cascade.Wrap(cascade.KindIntegrity, ErrInvalidSignature, "attest: signature does not verify")
	}
	return rec, nil
}

// fieldsFor returns the closed key list of a supported schema version.
func fieldsFor(version int) ([]string, bool) {
	if version == 1 {
		return AttestationFieldsV1, true
	}
	return nil, false
}

// DecodeCanonical parses canonical record bytes and refuses anything that
// is not exactly the canonical form of the record it decodes to: a key its
// schema_version does not list, a schema_version above the supported one,
// or any difference in order, spacing, time or array form is
// ErrInvalidRecord. It is the only way a consumer reads a record.
func DecodeCanonical(canon []byte) (AttestationRecord, error) {
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(canon, &keys); err != nil || keys == nil {
		return AttestationRecord{}, invalidRecord("not a JSON object")
	}
	var version int
	if err := json.Unmarshal(keys["schema_version"], &version); err != nil {
		return AttestationRecord{}, invalidRecord("schema_version is missing or not an integer")
	}
	fields, ok := fieldsFor(version)
	if !ok {
		return AttestationRecord{}, invalidRecord("schema_version is not supported")
	}
	for name := range keys {
		if !slices.Contains(fields, name) {
			return AttestationRecord{}, invalidRecord("field " + name + " is not in the schema")
		}
	}
	rec, err := decodeWire(canon)
	if err != nil {
		return AttestationRecord{}, err
	}
	again, err := CanonicalJSON(rec)
	if err != nil {
		return AttestationRecord{}, err
	}
	if !bytes.Equal(again, canon) {
		return AttestationRecord{}, invalidRecord("bytes are not canonical")
	}
	return rec, nil
}

// wireRecord is the decode shape: times stay strings until parsed.
type wireRecord struct {
	ArtifactHashes  []string    `json:"artifact_hashes"`
	AttestorClass   string      `json:"attestor_class"`
	CheckpointID    string      `json:"checkpoint_id"`
	CommitSHA       string      `json:"commit_sha"`
	EndedAt         string      `json:"ended_at"`
	Environment     Environment `json:"environment"`
	ExitStatus      int         `json:"exit_status"`
	ExpiresAt       string      `json:"expires_at"`
	JobSpecHash     string      `json:"job_spec_hash"`
	KeyID           string      `json:"key_id"`
	NodeID          string      `json:"node_id"`
	Nonce           string      `json:"nonce"`
	PolicyVersion   string      `json:"policy_version"`
	RepoID          string      `json:"repo_id"`
	ResultHashes    []string    `json:"result_hashes"`
	SchemaVersion   int         `json:"schema_version"`
	StartedAt       string      `json:"started_at"`
	TargetSelection string      `json:"target_selection"`
	ToolchainFP     string      `json:"toolchain_fp"`
	TreeHash        string      `json:"tree_hash"`
	OIDCTokenSHA256 string      `json:"oidc_token_sha256"`
	OIDCRunID       string      `json:"oidc_run_id"`
}

// decodeWire decodes canon into a record; canonical equality is checked by
// the caller.
func decodeWire(canon []byte) (AttestationRecord, error) {
	dec := json.NewDecoder(bytes.NewReader(canon))
	dec.DisallowUnknownFields()
	var w wireRecord
	if err := dec.Decode(&w); err != nil {
		return AttestationRecord{}, invalidRecord("fields do not decode")
	}
	var times [3]time.Time
	for i, s := range []string{w.EndedAt, w.ExpiresAt, w.StartedAt} {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return AttestationRecord{}, invalidRecord("a time field is not RFC 3339")
		}
		times[i] = t.UTC()
	}
	return AttestationRecord{
		SchemaVersion: w.SchemaVersion, ArtifactHashes: w.ArtifactHashes, AttestorClass: AttestorClass(w.AttestorClass),
		CheckpointID: w.CheckpointID, CommitSHA: w.CommitSHA, EndedAt: times[0], Environment: w.Environment,
		ExitStatus: w.ExitStatus, ExpiresAt: times[1], JobSpecHash: w.JobSpecHash, KeyID: w.KeyID, NodeID: w.NodeID,
		Nonce: w.Nonce, PolicyVersion: w.PolicyVersion, RepoID: w.RepoID, ResultHashes: w.ResultHashes,
		StartedAt: times[2], TargetSelection: w.TargetSelection, ToolchainFP: w.ToolchainFP, TreeHash: w.TreeHash,
		OIDCTokenSHA256: w.OIDCTokenSHA256, OIDCRunID: w.OIDCRunID,
	}, nil
}
