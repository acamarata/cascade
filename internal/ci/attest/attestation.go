package attest

// Purpose: the versioned AttestationRecord, its single canonical byte form
// (CanonicalJSON, DecodeCanonical), Attest over a custody signing function,
// RowID and the Passing rule (contract:ci-attestation).
//
// Inputs: a record built by the node that ran the acceptance re-run and a
// sign function bound to that node's NodeKeystore.Sign.
// Outputs: a SignedAttestation whose CanonicalJSON is exactly what was
// signed (after the domain prefix).
// Constraints: keys in AttestationFieldsV1 order, no whitespace, times as
// RFC 3339 UTC seconds, hash arrays sorted, absent arrays as []. Any zero
// required field is ErrInvalidRecord. The private key never reaches this
// package: Attest sees only a sign function.
// SPORT: internal.ci.attest.AttestationRecord/ADDED,
// internal.ci.attest.CanonicalJSON/ADDED, internal.ci.attest.Attest/ADDED,
// internal.ci.attest.RowID/ADDED (P1-CI-04).

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// AttestationSchemaVersion is the record schema version this build writes
// and the highest one it reads.
const AttestationSchemaVersion = 1

// AttestationFieldsV1 is the closed key list of schema version 1, in
// canonical key order. A later version appends names only (additive). The
// two oidc names are v1 fields, empty for node-built records.
var AttestationFieldsV1 = []string{
	"artifact_hashes", "attestor_class", "checkpoint_id", "commit_sha", "ended_at", "environment", "exit_status",
	"expires_at", "job_spec_hash", "key_id", "node_id", "nonce", "policy_version", "repo_id", "result_hashes",
	"schema_version", "started_at", "target_selection", "toolchain_fp", "tree_hash", "oidc_token_sha256", "oidc_run_id",
}

// signingDomain separates attestation signatures from every other payload
// the node identity key signs (heartbeats, rotations, ssh userauth).
const signingDomain = "cascade.ci.attestation.v1\x1f"

// Environment is the executor provenance a record carries, mirroring
// internal/ci's Environment (this leaf package may not import it).
type Environment struct {
	Ephemeral      bool   `json:"ephemeral"`
	IsolationClass string `json:"isolation_class"`
	NodeID         string `json:"node_id"`
	RunnerKind     string `json:"runner_kind"`
	ToolchainFP    string `json:"toolchain_fp"`
}

// AttestationRecord is one CI attestation. ResultHashes entries are
// "<plan kind>:<hex digest>"; ArtifactHashes entries are opaque digests.
type AttestationRecord struct {
	SchemaVersion   int           `json:"schema_version"`
	ArtifactHashes  []string      `json:"artifact_hashes"`
	AttestorClass   AttestorClass `json:"attestor_class"`
	CheckpointID    string        `json:"checkpoint_id"`
	CommitSHA       string        `json:"commit_sha"`
	EndedAt         time.Time     `json:"ended_at"`
	Environment     Environment   `json:"environment"`
	ExitStatus      int           `json:"exit_status"`
	ExpiresAt       time.Time     `json:"expires_at"`
	JobSpecHash     string        `json:"job_spec_hash"`
	KeyID           string        `json:"key_id"`
	NodeID          string        `json:"node_id"`
	Nonce           string        `json:"nonce"`
	PolicyVersion   string        `json:"policy_version"`
	RepoID          string        `json:"repo_id"`
	ResultHashes    []string      `json:"result_hashes"`
	StartedAt       time.Time     `json:"started_at"`
	TargetSelection string        `json:"target_selection"`
	ToolchainFP     string        `json:"toolchain_fp"`
	TreeHash        string        `json:"tree_hash"`
	OIDCTokenSHA256 string        `json:"oidc_token_sha256"`
	OIDCRunID       string        `json:"oidc_run_id"`
}

// SignedAttestation is a record plus the exact bytes that were signed.
// Verification reads CanonicalJSON only; Record is a convenience copy and
// is never trusted. Signature is base64 (RawStdEncoding) Ed25519.
type SignedAttestation struct {
	Record        AttestationRecord
	CanonicalJSON []byte
	Signature     string
}

// canonicalTime renders t as RFC 3339 UTC with whole seconds.
func canonicalTime(t time.Time) string {
	return t.UTC().Truncate(time.Second).Format(time.RFC3339)
}

// sortedHashes returns a sorted copy of hs, never nil (encodes as []).
func sortedHashes(hs []string) []string {
	out := append([]string{}, hs...)
	slices.Sort(out)
	return out
}

// fieldValues maps every v1 key to the value its canonical form encodes.
func fieldValues(r AttestationRecord) map[string]any {
	return map[string]any{
		"artifact_hashes": sortedHashes(r.ArtifactHashes), "attestor_class": string(r.AttestorClass),
		"checkpoint_id": r.CheckpointID, "commit_sha": r.CommitSHA, "ended_at": canonicalTime(r.EndedAt),
		"environment": r.Environment, "exit_status": r.ExitStatus, "expires_at": canonicalTime(r.ExpiresAt),
		"job_spec_hash": r.JobSpecHash, "key_id": r.KeyID, "node_id": r.NodeID, "nonce": r.Nonce,
		"policy_version": r.PolicyVersion, "repo_id": r.RepoID, "result_hashes": sortedHashes(r.ResultHashes),
		"schema_version": r.SchemaVersion, "started_at": canonicalTime(r.StartedAt),
		"target_selection": r.TargetSelection, "toolchain_fp": r.ToolchainFP, "tree_hash": r.TreeHash,
		"oidc_token_sha256": r.OIDCTokenSHA256, "oidc_run_id": r.OIDCRunID,
	}
}

// CanonicalJSON returns r's single canonical byte form. A schema_version
// other than AttestationSchemaVersion or any zero required field
// (policy_version, checkpoint_id, nonce, expires_at, commit_sha, tree_hash,
// node_id, key_id) is ErrInvalidRecord.
func CanonicalJSON(r AttestationRecord) ([]byte, error) {
	if err := checkRequired(r); err != nil {
		return nil, err
	}
	values := fieldValues(r)
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, name := range AttestationFieldsV1 {
		v, err := json.Marshal(values[name])
		if err != nil {
			return nil, invalidRecord("field " + name + " does not encode")
		}
		if i > 0 {
			buf.WriteByte(',')
		}
		buf.WriteString(`"` + name + `":`)
		buf.Write(v)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// checkRequired refuses a record missing any field verification binds.
func checkRequired(r AttestationRecord) error {
	if r.SchemaVersion != AttestationSchemaVersion {
		return invalidRecord("schema_version is not 1")
	}
	required := []struct{ name, value string }{
		{"policy_version", r.PolicyVersion}, {"checkpoint_id", r.CheckpointID}, {"nonce", r.Nonce},
		{"commit_sha", r.CommitSHA}, {"tree_hash", r.TreeHash}, {"node_id", r.NodeID}, {"key_id", r.KeyID},
	}
	for _, f := range required {
		if strings.TrimSpace(f.value) == "" {
			return invalidRecord("required field " + f.name + " is empty")
		}
	}
	if r.ExpiresAt.IsZero() {
		return invalidRecord("required field expires_at is zero")
	}
	return nil
}

// Attest canonicalizes r and signs the domain-separated canonical bytes
// with sign, which production binds to NodeKeystore.Sign for r.NodeID, so
// the private key never leaves custody.
func Attest(r AttestationRecord, sign func(payload []byte) ([]byte, error)) (SignedAttestation, error) {
	if sign == nil {
		return SignedAttestation{}, cascade.New(cascade.KindInvalidInput, "attest: Attest requires a sign function")
	}
	canon, err := CanonicalJSON(r)
	if err != nil {
		return SignedAttestation{}, err
	}
	sig, err := sign(signingPayload(canon))
	if err != nil {
		return SignedAttestation{}, cascade.Wrap(cascade.KindUnavailable, err, "attest: sign the attestation")
	}
	if len(sig) != ed25519.SignatureSize {
		return SignedAttestation{}, cascade.Newf(cascade.KindIntegrity, "attest: signature has length %d, want %d", len(sig), ed25519.SignatureSize)
	}
	rec, err := DecodeCanonical(canon)
	if err != nil {
		return SignedAttestation{}, err
	}
	return SignedAttestation{Record: rec, CanonicalJSON: canon, Signature: base64.RawStdEncoding.EncodeToString(sig)}, nil
}

// signingPayload is the exact byte string an attestation signature covers.
func signingPayload(canon []byte) []byte {
	return append([]byte(signingDomain), canon...)
}

// RowID is the stable ci_attestation row id: hex SHA-256 of the canonical
// bytes.
func RowID(sa SignedAttestation) string {
	sum := sha256.Sum256(sa.CanonicalJSON)
	return hex.EncodeToString(sum[:])
}

// Passing reports whether r counts as a passing run of a plan with the
// given kinds: exit_status 0 and a non-empty "<kind>:<digest>" result hash
// for every kind. An empty plan never passes (nothing was proven), and a kind
// containing ':' never matches (it would be ambiguous with the separator).
func (r AttestationRecord) Passing(planKinds []string) bool {
	if r.ExitStatus != 0 || len(planKinds) == 0 {
		return false
	}
	for _, kind := range planKinds {
		if kind == "" || strings.Contains(kind, ":") || !hasResultFor(r.ResultHashes, kind) {
			return false
		}
	}
	return true
}

// hasResultFor reports whether hashes holds a non-empty digest for kind; a
// digest containing ':' belongs to some other "<kind>:<more>" entry.
func hasResultFor(hashes []string, kind string) bool {
	for _, h := range hashes {
		if digest, ok := strings.CutPrefix(h, kind+":"); ok && digest != "" && !strings.Contains(digest, ":") {
			return true
		}
	}
	return false
}

// invalidRecord wraps ErrInvalidRecord with a fixed reason.
func invalidRecord(reason string) error {
	return cascade.Wrap(cascade.KindInvalidInput, ErrInvalidRecord, "attest: invalid record: "+reason)
}
