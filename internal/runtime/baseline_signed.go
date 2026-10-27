package runtime

// Purpose: the signed (v2) config baseline: the record shape that carries
//
//	the attestation of the config.accept_loosening call that accepted one
//	exact config file, and the boot-time decision whether such a record
//	admits a config looser than MostRestrictiveDefaults.
//
// Inputs: a persisted baselineRecordV1, the SHA-256 of the raw config.toml
//
//	bytes, and the injected BaselineVerifier (production: the attestation
//	signature checked against the custody-bound trust root).
//
// Outputs: BaselineAccepted, BaselineUnsignedLooser or BaselineProofInvalid.
// Constraints: fail closed. A nil verifier, a missing proof, a method other
//
//	than config.accept_loosening, params that do not decode to exactly
//	{"config_sha256": X}, an X that differs from the file digest or from the
//	record's own config_sha256, and a proof the verifier refuses all admit
//	nothing looser than the defaults.
//
// SPORT: runtime/boot-baseline-check (signed baseline record).

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"

	"github.com/acamarata/cascade/pkg/cascade"
)

// AcceptLooseningMethod is the one elevated RPC whose attestation a signed
// baseline record carries.
const AcceptLooseningMethod = "config.accept_loosening"

// baselineRecordVersionSigned is the record version that carries a proof.
const baselineRecordVersionSigned = 2

// BaselineProof is the attested call that accepted one exact config file:
// the elevated method, the byte-exact params the attestation covered, and
// the attestation itself.
type BaselineProof struct {
	Method      string
	RawParams   []byte
	Attestation json.RawMessage
}

// BaselineFile is the config file a boot check runs against: the SHA-256 of
// its raw bytes, and Signed, which returns the guarded values those exact
// bytes declare with no environment override (false when they do not load).
// The signature covers the bytes only, so Signed is what a signed record
// admits; a record's own sections_hash is never trusted for admission.
type BaselineFile struct {
	SHA256 string
	Signed func() (EffectiveConfig, bool)
}

// BaselineVerifier checks a proof's attestation against the trust root. A
// nil error is the only admission.
type BaselineVerifier func(p BaselineProof) error

// baselineProofDoc is the persisted form of BaselineProof. RawParams is
// base64 in JSON ([]byte's encoding), so its bytes survive exactly.
type baselineProofDoc struct {
	Method      string          `json:"method"`
	RawParams   []byte          `json:"raw_params"`
	Attestation json.RawMessage `json:"attestation"`
}

// acceptParams is the only params shape config.accept_loosening takes.
type acceptParams struct {
	ConfigSHA256 string `json:"config_sha256"`
}

// configDigestPattern is a SHA-256 in lowercase hex.
var configDigestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ConfigDigest is the SHA-256 (lowercase hex) of raw config file bytes, the
// value config.accept_loosening's params carry and a v2 record covers.
func ConfigDigest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// DecodeAcceptParams decodes config.accept_loosening params strictly: one
// field, config_sha256, a 64-character lowercase hex digest, nothing else.
func DecodeAcceptParams(raw []byte) (string, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var p acceptParams
	if err := dec.Decode(&p); err != nil {
		return "", false
	}
	if dec.More() {
		return "", false
	}
	if !configDigestPattern.MatchString(p.ConfigSHA256) {
		return "", false
	}
	return p.ConfigSHA256, true
}

// SetVerifier installs the proof verifier. Until it is called no v2 record
// admits anything and Accept refuses.
func (b *BaselineChecker) SetVerifier(v BaselineVerifier) { b.verify = v }

// signedOutcome decides a record whose sections hash matches onDisk, a
// config looser than the defaults. The proof must cover the file's bytes,
// and the guarded values those signed bytes declare must be exactly
// onDisk's: an environment override, a swapped file or a forged
// sections_hash otherwise run unsigned values.
func (b *BaselineChecker) signedOutcome(rec baselineRecordV1, file BaselineFile, onDisk EffectiveConfig) BaselineOutcome {
	if rec.Version < baselineRecordVersionSigned || rec.Proof == nil {
		return BaselineUnsignedLooser
	}
	if b.proofCovers(BaselineProof{Method: rec.Proof.Method, RawParams: rec.Proof.RawParams, Attestation: rec.Proof.Attestation}, file.SHA256, rec.ConfigSHA256) != nil {
		return BaselineProofInvalid
	}
	if file.Signed == nil {
		return BaselineProofInvalid
	}
	signed, ok := file.Signed()
	if !ok {
		return BaselineProofInvalid
	}
	if ComputeSectionsHash(signed) != ComputeSectionsHash(onDisk) {
		return BaselineUnsignedLooser
	}
	return BaselineAccepted
}

// proofCovers returns nil only when p is a verified config.accept_loosening
// proof whose params name exactly fileSHA, and recordSHA (the digest the
// record states) is the same value.
func (b *BaselineChecker) proofCovers(p BaselineProof, fileSHA, recordSHA string) error {
	if b.verify == nil {
		return errBaselineProof("no baseline verifier is installed")
	}
	if p.Method != AcceptLooseningMethod {
		return errBaselineProof("proof is for another method")
	}
	covered, ok := DecodeAcceptParams(p.RawParams)
	if !ok {
		return errBaselineProof("proof params are not {\"config_sha256\": <digest>}")
	}
	if fileSHA == "" || covered != fileSHA || covered != recordSHA {
		return errBaselineProof("proof covers other config bytes")
	}
	if len(p.Attestation) == 0 {
		return errBaselineProof("proof carries no attestation")
	}
	if err := b.verify(p); err != nil {
		return errBaselineProof("proof attestation does not verify: " + err.Error())
	}
	return nil
}

// baselineProofError reports why a proof does not admit a config.
type baselineProofError struct{ reason string }

func (e *baselineProofError) Error() string { return "runtime: baseline proof refused: " + e.reason }

func errBaselineProof(reason string) error { return &baselineProofError{reason: reason} }

// baselineDetails is the status detail of each outcome.
var baselineDetails = map[BaselineOutcome]string{
	BaselineOK:                "baseline_ok",
	BaselineTightened:         "baseline_tightened",
	BaselineMissing:           "baseline_missing",
	BaselineDivergent:         "baseline_divergent",
	BaselineFirstBootAccepted: "baseline_first_boot_accepted",
	BaselineAccepted:          "baseline_accepted",
	BaselineUnsignedLooser:    "baseline_unsigned_looser",
	BaselineProofInvalid:      "baseline_proof_invalid",
	BaselineCorrupt:           "baseline_corrupt",
}

// Detail is the outcome's status detail string. An unknown outcome reads
// as divergent, never as ok.
func (o BaselineOutcome) Detail() string {
	if d, ok := baselineDetails[o]; ok {
		return d
	}
	return baselineDetails[BaselineDivergent]
}

// Degraded reports whether the outcome runs the most-restrictive values.
// Only the four admitting outcomes are not degraded.
func (o BaselineOutcome) Degraded() bool {
	switch o {
	case BaselineOK, BaselineTightened, BaselineFirstBootAccepted, BaselineAccepted:
		return false
	case BaselineMissing, BaselineDivergent, BaselineUnsignedLooser, BaselineProofInvalid, BaselineCorrupt:
		return true
	default:
		return true
	}
}

// recordState is what reading the baseline record found.
type recordState int

const (
	recordFound recordState = iota
	// recordAbsent: no store, or the store reports the key not found.
	recordAbsent
	// recordCorrupt: the row exists but could not be read or decoded.
	recordCorrupt
)

// readRecord reads and decodes the persisted baseline record. Only a nil
// store or a KindNotFound read is "absent" (first boot); any other read
// error and an undecodable row are "corrupt", which refuses and never
// overwrites the row.
func (b *BaselineChecker) readRecord(ctx context.Context) (baselineRecordV1, string, recordState) {
	if b.store == nil {
		return baselineRecordV1{}, "", recordAbsent
	}
	data, err := b.store.Get(ctx, auditNamespace, baselineRecordKey)
	if err != nil {
		if cascade.HasKind(err, cascade.KindNotFound) {
			return baselineRecordV1{}, "", recordAbsent
		}
		return baselineRecordV1{}, "", recordCorrupt
	}
	var rec baselineRecordV1
	if err := json.Unmarshal(data, &rec); err != nil {
		return baselineRecordV1{}, "", recordCorrupt
	}
	warning := ""
	switch {
	case rec.Version > baselineRecordVersionSigned:
		warning = "baseline record schema_version is newer than this binary understands; reading tolerantly"
	case rec.Version == baselineRecordVersionSigned && rec.Proof == nil:
		warning = "baseline record is v2 but carries no proof; it admits nothing looser than the defaults"
	}
	return rec, warning, recordFound
}

// readSnapshot reads and decodes the adjacent EffectiveConfig snapshot;
// any absence or decode failure reports hasSnapshot=false.
func (b *BaselineChecker) readSnapshot(ctx context.Context) (EffectiveConfig, bool) {
	if b.store == nil {
		return EffectiveConfig{}, false
	}
	data, err := b.store.Get(ctx, auditNamespace, baselineSnapshotKey)
	if err != nil {
		return EffectiveConfig{}, false
	}
	var snap EffectiveConfig
	if err := json.Unmarshal(data, &snap); err != nil {
		return EffectiveConfig{}, false
	}
	return snap, true
}

// namesEverySyncDomain reports whether next names every [sync] domain prev
// names: a dropped domain runs its compiled class, which may be looser.
func namesEverySyncDomain(prev, next EffectiveConfig) bool {
	for d := range prev.Sync.Classes {
		if _, ok := next.Sync.Classes[d]; !ok {
			return false
		}
	}
	return true
}

// fallbackBase is onDisk plus every [sync] domain the snapshot names, so a
// degraded boot also pins a domain the file dropped (RestrictiveFallback
// pins each named domain to local-only). The snapshot is unsigned, but it
// can only add domains to pin, never loosen one.
func (b *BaselineChecker) fallbackBase(ctx context.Context, onDisk EffectiveConfig) EffectiveConfig {
	snap, ok := b.readSnapshot(ctx)
	if !ok || len(snap.Sync.Classes) == 0 {
		return onDisk
	}
	out := onDisk
	out.Sync.Classes = make(map[string]string, len(onDisk.Sync.Classes)+len(snap.Sync.Classes))
	for d := range snap.Sync.Classes {
		out.Sync.Classes[d] = restrictiveSyncClass
	}
	for d, c := range onDisk.Sync.Classes {
		out.Sync.Classes[d] = c
	}
	return out
}
