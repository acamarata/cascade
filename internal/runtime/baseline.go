package runtime

// Purpose: the boot-time config baseline check (08 §3, §D-26/§D-39): a
//
//	canonical SHA-256 over the guarded sections of the resolved config is
//	compared with the record persisted in the audit domain. A config looser
//	than MostRestrictiveDefaults is admitted only by a matching v2 record
//	whose proof verifies (baseline_signed.go); a missing, unsigned-looser,
//	proof-invalid or divergent baseline fails closed: most-restrictive
//	values, a config.policy.divergent event, a doctor-error audit record.
//	A tighter config at or under the defaults rewrites the record.
//
// Inputs: the resolved EffectiveConfig, the config file's digest, and an
//
//	injected provider.Store, Clock and BaselineVerifier.
//
// Outputs: a BaselineResult naming the outcome and the EffectiveConfig the
//
//	caller must run with.
//
// Constraints: the reader tolerates unknown record fields and versions
//
//	above 2 (warning only). Never calls a bare time.Now (Art.7.3).
//
// SPORT: runtime/boot-baseline-check.

import (
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// baselineRecordKey is the fixed single-record key this ticket persists
// the baseline under, inside the "audit" namespace (R-14.8: baseline
// hash+signature lives in the audit domain via the Store API — the
// flat-file data_dir/baseline.json design is stricken, §D-26).
const baselineRecordKey = "config_baseline"

// baselineSnapshotKey holds the EffectiveConfig JSON the last-persisted
// baseline hash was computed from — an implementation necessity this
// file adds alongside the ticket-specified hash-only record: a SHA-256
// hash is one-way by construction, so "is the new config tighter or
// looser than the recorded baseline" is not computable from the hash
// alone (only "does it match" is). The audit-visible, sign-able record
// (baselineRecordV1, D/S-07.T6's future signature target) stays exactly
// the contract's literal {v, sections_hash, sections} schema in the
// baselineRecordKey slot; this second, internal-only key is what makes
// direction detection possible without bloating or changing the shape of
// the record a future signer would sign.
const baselineSnapshotKey = "config_baseline_snapshot"

// baselineGuardedSections is the fixed, ordered set of section names the
// canonical hash covers — the record schema's own "sections" field
// (baselineRecordV1.Sections) is redundant with this constant but kept in
// the persisted record per the ticket contract's literal record schema,
// and cross-checked against it on read (an unexpected sections list is
// itself treated as divergent, fail-closed).
var baselineGuardedSections = []string{"policy", "secrets", "sync", "nodes", "conductor", "elevation"}

// baselineRecordV1 is the baseline record schema: the v1 shape {v: 1,
// sections_hash, sections} plus the v2 fields (config_sha256, proof) that
// baseline_signed.go reads. json.Unmarshal ignores fields it does not know,
// so a newer record still decodes; Version is a plain int so v>2 is
// detected without error.
type baselineRecordV1 struct {
	Version      int               `json:"v"`
	SectionsHash string            `json:"sections_hash"`
	Sections     []string          `json:"sections"`
	ConfigSHA256 string            `json:"config_sha256,omitempty"`
	Proof        *baselineProofDoc `json:"proof,omitempty"`
}

// BaselineOutcome names the cases the boot-time check distinguishes.
type BaselineOutcome int

const (
	// BaselineOK means the on-disk config matches the persisted baseline
	// and is not looser than MostRestrictiveDefaults.
	BaselineOK BaselineOutcome = iota
	// BaselineTightened means the on-disk config is strictly tighter than
	// the persisted baseline and not looser than MostRestrictiveDefaults;
	// the record is rewritten to the new hash.
	BaselineTightened
	// BaselineMissing means no usable record exists and the on-disk config
	// is looser than MostRestrictiveDefaults; fail closed.
	BaselineMissing
	// BaselineDivergent means a record exists but the on-disk config does
	// not match it and is not a provable tightening; fail closed.
	BaselineDivergent
	// BaselineFirstBootAccepted means no record existed and the on-disk
	// config is not looser than MostRestrictiveDefaults; a v1 record was
	// written.
	BaselineFirstBootAccepted
	// BaselineAccepted means the on-disk config is looser than the
	// defaults and a v2 record whose proof verifies covers its exact bytes.
	BaselineAccepted
	// BaselineUnsignedLooser means the record matches a looser on-disk
	// config but carries no proof (v1, or v2 without one); fail closed.
	BaselineUnsignedLooser
	// BaselineProofInvalid means the record matches a looser on-disk config
	// but its proof does not verify or covers other bytes; fail closed.
	BaselineProofInvalid
	// BaselineCorrupt means a record exists but cannot be read or decoded;
	// fail closed and leave the row as it is.
	BaselineCorrupt
)

// BaselineResult is BaselineChecker.Check's full report.
type BaselineResult struct {
	Outcome      BaselineOutcome
	ExpectedHash string
	ActualHash   string
	// Effective is the config the caller must actually run with: the
	// on-disk EffectiveConfig for BaselineOK/BaselineTightened, or
	// MostRestrictiveDefaults() for BaselineMissing/BaselineDivergent
	// (fail-closed, §D-39).
	Effective EffectiveConfig
	// VersionWarning is set when the persisted record's schema version is
	// newer than 1 (forward-compat: tolerated, not an error).
	VersionWarning string
}

// BaselineChecker runs the boot-time baseline check exactly once, after
// config load and before the daemon serves requests (08 §3 / this
// ticket's contract).
type BaselineChecker struct {
	store  provider.Store
	clock  Clock
	events EventPublisher
	audit  AuditRecorder
	// verify checks a v2 record's proof. Nil admits no v2 record.
	verify BaselineVerifier
	// boot is the effective config the last CheckFile told the caller to
	// run; restart-required keys are measured against it for the life of
	// the process (nil: no check ran, MostRestrictiveDefaults stands in).
	boot *EffectiveConfig
}

// NewBaselineChecker builds a BaselineChecker. events/audit follow the
// same nil-tolerant conventions as HotReloader (see hotreload_events.go).
func NewBaselineChecker(store provider.Store, clock Clock, events EventPublisher, audit AuditRecorder) *BaselineChecker {
	if events == nil {
		events = DiscardEventPublisher{}
	}
	return &BaselineChecker{store: store, clock: clock, events: events, audit: audit}
}

// eventPolicyDivergent is 08 §3's boot-time divergence event.
const eventPolicyDivergent = "config.policy.divergent"

// auditKindDoctorError is the doctor-error record kind persisted on a
// missing/divergent baseline (R-14.8: "cascade doctor reports an error"
// backed by a persisted audit record `cascade doctor` reads).
const auditKindDoctorError = "doctor_error"

// auditKindBaselineUpdate is the record kind persisted when the baseline
// is rewritten to a new (tighter, or first-ever) hash.
const auditKindBaselineUpdate = "baseline_update"

// Check runs the boot-time baseline check against onDisk without a file
// digest, so no v2 record can admit a looser config. CheckFile is the
// daemon's entry point.
func (b *BaselineChecker) Check(ctx context.Context, onDisk EffectiveConfig) BaselineResult {
	return b.CheckFile(ctx, onDisk, BaselineFile{})
}

// CheckFile runs the boot-time baseline check against onDisk, the resolved
// EffectiveConfig the daemon will run (environment overrides included), and
// file, the config file's exact bytes. A config looser than
// MostRestrictiveDefaults is admitted only by a v2 record whose proof
// verifies over file.SHA256 and whose signed bytes declare exactly onDisk's
// guarded values; every other looser case returns MostRestrictiveDefaults.
func (b *BaselineChecker) CheckFile(ctx context.Context, onDisk EffectiveConfig, file BaselineFile) BaselineResult {
	res := b.checkFile(ctx, onDisk, file)
	boot := res.Effective
	b.boot = &boot
	return res
}

func (b *BaselineChecker) checkFile(ctx context.Context, onDisk EffectiveConfig, file BaselineFile) BaselineResult {
	actualHash := ComputeSectionsHash(onDisk)
	atFloor := len(CompareSecurity(MostRestrictiveDefaults(), onDisk)) == 0

	record, versionWarning, state := b.readRecord(ctx)
	if state == recordCorrupt {
		return b.failClosed(ctx, b.fallbackBase(ctx, onDisk), BaselineCorrupt, "", actualHash, map[string]interface{}{"reason": "baseline_corrupt"})
	}
	if state == recordAbsent {
		// With no store nothing could be recorded, so nothing is accepted;
		// a record that did not persist is the same as none.
		if atFloor && b.store != nil {
			if err := b.persistBaselineUpdate(ctx, actualHash, onDisk); err != nil {
				return b.failClosed(ctx, onDisk, BaselineMissing, "", actualHash, map[string]interface{}{"reason": "baseline_missing", "error": err.Error()})
			}
			return BaselineResult{Outcome: BaselineFirstBootAccepted, ActualHash: actualHash, Effective: onDisk}
		}
		return b.failClosed(ctx, onDisk, BaselineMissing, "", actualHash, map[string]interface{}{"reason": "baseline_missing"})
	}

	if record.SectionsHash == actualHash {
		if atFloor {
			return BaselineResult{Outcome: BaselineOK, ExpectedHash: record.SectionsHash, ActualHash: actualHash, Effective: onDisk, VersionWarning: versionWarning}
		}
		outcome := b.signedOutcome(record, file, onDisk)
		if outcome == BaselineAccepted {
			return BaselineResult{Outcome: outcome, ExpectedHash: record.SectionsHash, ActualHash: actualHash, Effective: onDisk, VersionWarning: versionWarning}
		}
		return b.failClosed(ctx, b.fallbackBase(ctx, onDisk), outcome, record.SectionsHash, actualHash, map[string]interface{}{
			"reason": outcome.Detail(), "expected_hash": record.SectionsHash, "actual_hash": actualHash,
		})
	}

	// Direction (tighter vs looser) is not computable from the hash alone,
	// so it is read from the adjacent snapshot. The snapshot is unsigned,
	// so it can only ever prove a tightening down to a config that is
	// itself not looser than the defaults; a looser config is admitted by
	// a matching signed record alone.
	// A [sync] domain the snapshot names and onDisk drops is not a
	// tightening: CompareSecurity reads it as local-only, but the sync
	// runtime falls back to the domain's compiled class.
	snapshot, hasSnapshot := b.readSnapshot(ctx)
	if atFloor && hasSnapshot && namesEverySyncDomain(snapshot, onDisk) && len(CompareSecurity(snapshot, onDisk)) == 0 {
		if err := b.persistBaselineUpdate(ctx, actualHash, onDisk); err != nil {
			return b.failClosed(ctx, b.fallbackBase(ctx, onDisk), BaselineDivergent, record.SectionsHash, actualHash, map[string]interface{}{"reason": "baseline_divergent", "error": err.Error()})
		}
		return BaselineResult{Outcome: BaselineTightened, ExpectedHash: record.SectionsHash, ActualHash: actualHash, Effective: onDisk, VersionWarning: versionWarning}
	}

	return b.failClosed(ctx, b.fallbackBase(ctx, onDisk), BaselineDivergent, record.SectionsHash, actualHash, map[string]interface{}{
		"reason": "baseline_divergent", "expected_hash": record.SectionsHash, "actual_hash": actualHash,
	})
}

// failClosed applies §D-39's fail-closed treatment: most-restrictive
// shipped defaults, config.policy.divergent event, doctor-error audit
// record.
func (b *BaselineChecker) failClosed(ctx context.Context, onDisk EffectiveConfig, outcome BaselineOutcome, expected, actual string, fields map[string]interface{}) BaselineResult {
	b.events.Publish(ctx, eventPolicyDivergent, fields)
	if b.audit != nil {
		_ = b.audit.Record(ctx, auditKindDoctorError, fields)
	}
	eff := RestrictiveFallback(onDisk)
	// Tightest of the default and the on-disk threshold, per field.
	eff.Secrets = restrictiveSecrets(onDisk.Secrets)
	return BaselineResult{Outcome: outcome, ExpectedHash: expected, ActualHash: actual, Effective: eff}
}

// persistBaselineUpdate rewrites the baseline record to hash and its
// adjacent snapshot to effective, so the next boot has something to compare
// against. It returns the first store error: a record that did not persist
// must not be reported as accepted.
func (b *BaselineChecker) persistBaselineUpdate(ctx context.Context, hash string, effective EffectiveConfig) error {
	if b.store == nil {
		return cascade.New(cascade.KindUnavailable, "baseline: no store to record the baseline in")
	}
	rec, err := json.Marshal(baselineRecordV1{Version: 1, SectionsHash: hash, Sections: baselineGuardedSections})
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "baseline: encode record")
	}
	snap, err := json.Marshal(effective)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "baseline: encode snapshot")
	}
	if err := b.store.Put(ctx, auditNamespace, baselineRecordKey, rec); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "baseline: write record")
	}
	if err := b.store.Put(ctx, auditNamespace, baselineSnapshotKey, snap); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "baseline: write snapshot")
	}
	if b.audit != nil {
		_ = b.audit.Record(ctx, auditKindBaselineUpdate, map[string]interface{}{"sections_hash": hash})
	}
	return nil
}
