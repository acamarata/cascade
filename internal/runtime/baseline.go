package runtime

// Purpose: the boot-time divergent-baseline check (08 §3 / 11-ROUND3-
//
//	DELTAS.md §D-26/§D-39): computes a canonical SHA-256 over the six
//	CompareSecurity-guarded sections of the resolved config, compares it
//	against the last-persisted baseline record in the audit domain
//	(pkg/provider.Store), and fails closed — most-restrictive shipped
//	defaults, a config.policy.divergent event, a doctor-error audit
//	record — whenever the baseline is missing or the on-disk config is
//	looser than it. A tighter on-disk config is not a divergence: it
//	proceeds normally and the baseline record is rewritten to the new,
//	tighter hash.
//
// Inputs: the resolved EffectiveConfig (extractEffectiveConfig(cfg)) and
//
//	an injected provider.Store + Clock.
//
// Outputs: a BaselineResult naming the outcome; on Divergent/Missing, the
//
//	EffectiveConfig to actually run with (most-restrictive shipped
//	defaults, never the looser on-disk values).
//
// Constraints: the reader tolerates unknown record fields and a
//
//	schema_version greater than 1 without error (forward-compat for a
//	later v2 signed record) — RawFields on baselineRecordV1 carries
//	anything this reader does not recognise straight through unexamined,
//	proven by baseline_test.go decoding a hand-built v2 fixture with a
//	`sig` field. Never calls a bare time.Now (Clock-injected, Art.7.3).
//
// SPORT: runtime/boot-baseline-check (ADD, placeholder per T-8 sport_updates).

import (
	"context"
	"encoding/json"

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

// baselineRecordV1 is the v1 baseline record schema per the ticket
// contract verbatim: {v: 1, sections_hash: "<sha256-hex>", sections:
// [...]}. json.Unmarshal ignores fields it does not know about by
// default, which is exactly the forward-compat property a v2 reader
// needs — no RawFields capture is required to satisfy "tolerates unknown
// fields", but Version is read as a plain int so v>1 is detected without
// error (Reader MUST tolerate... v > 1 without error, warning log only).
type baselineRecordV1 struct {
	Version      int      `json:"v"`
	SectionsHash string   `json:"sections_hash"`
	Sections     []string `json:"sections"`
}

// BaselineOutcome names the four cases the boot-time check distinguishes.
type BaselineOutcome int

const (
	// BaselineOK means the on-disk config matches (or is tighter than)
	// the persisted baseline; proceed normally with the on-disk config.
	BaselineOK BaselineOutcome = iota
	// BaselineTightened means the on-disk config is strictly tighter than
	// the persisted baseline; proceed normally, and the baseline record
	// is rewritten to the new, tighter hash.
	BaselineTightened
	// BaselineMissing means no baseline record exists yet; fail closed.
	BaselineMissing
	// BaselineDivergent means a baseline record exists but the on-disk
	// config is looser than it; fail closed.
	BaselineDivergent
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

// Check runs the boot-time baseline check against onDisk (the resolved
// current EffectiveConfig).
func (b *BaselineChecker) Check(ctx context.Context, onDisk EffectiveConfig) BaselineResult {
	actualHash := ComputeSectionsHash(onDisk)

	record, versionWarning, found, err := b.readRecord(ctx)
	if err != nil || !found {
		return b.failClosed(ctx, BaselineMissing, "", actualHash, map[string]interface{}{"reason": "baseline_missing"})
	}

	if record.SectionsHash == actualHash {
		return BaselineResult{Outcome: BaselineOK, ExpectedHash: record.SectionsHash, ActualHash: actualHash, Effective: onDisk, VersionWarning: versionWarning}
	}

	// Direction (tighter vs looser) is not computable from the hash
	// alone (SHA-256 is one-way), so it is read from the adjacent
	// snapshot key (see baselineSnapshotKey's doc comment). No usable
	// snapshot (e.g. a legacy pre-snapshot record) is treated
	// conservatively as divergent — a hash mismatch this checker cannot
	// prove is a tightening is fail-closed, never assumed safe.
	snapshot, hasSnapshot := b.readSnapshot(ctx)
	if hasSnapshot && len(CompareSecurity(snapshot, onDisk)) == 0 {
		b.persistBaselineUpdate(ctx, actualHash, onDisk)
		return BaselineResult{Outcome: BaselineTightened, ExpectedHash: record.SectionsHash, ActualHash: actualHash, Effective: onDisk, VersionWarning: versionWarning}
	}

	return b.failClosed(ctx, BaselineDivergent, record.SectionsHash, actualHash, map[string]interface{}{
		"reason": "baseline_divergent", "expected_hash": record.SectionsHash, "actual_hash": actualHash,
	})
}

// failClosed applies §D-39's fail-closed treatment: most-restrictive
// shipped defaults, config.policy.divergent event, doctor-error audit
// record.
func (b *BaselineChecker) failClosed(ctx context.Context, outcome BaselineOutcome, expected, actual string, fields map[string]interface{}) BaselineResult {
	b.events.Publish(ctx, eventPolicyDivergent, fields)
	if b.audit != nil {
		_ = b.audit.Record(ctx, auditKindDoctorError, fields)
	}
	return BaselineResult{Outcome: outcome, ExpectedHash: expected, ActualHash: actual, Effective: MostRestrictiveDefaults()}
}

// persistBaselineUpdate rewrites the baseline record to hash and its
// adjacent snapshot to effective, so the NEXT boot's divergence check has
// something to compare against.
func (b *BaselineChecker) persistBaselineUpdate(ctx context.Context, hash string, effective EffectiveConfig) {
	if b.store == nil {
		return
	}
	rec := baselineRecordV1{Version: 1, SectionsHash: hash, Sections: baselineGuardedSections}
	if data, err := json.Marshal(rec); err == nil {
		_ = b.store.Put(ctx, auditNamespace, baselineRecordKey, data)
	}
	if data, err := json.Marshal(effective); err == nil {
		_ = b.store.Put(ctx, auditNamespace, baselineSnapshotKey, data)
	}
	if b.audit != nil {
		_ = b.audit.Record(ctx, auditKindBaselineUpdate, map[string]interface{}{"sections_hash": hash})
	}
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

// readRecord reads and decodes the persisted baseline record. A missing
// key (provider.KindNotFound) or a nil store both report found=false,
// never an error the caller must distinguish from "genuinely absent" —
// both mean the same fail-closed treatment.
func (b *BaselineChecker) readRecord(ctx context.Context) (baselineRecordV1, string, bool, error) {
	if b.store == nil {
		return baselineRecordV1{}, "", false, nil
	}
	data, err := b.store.Get(ctx, auditNamespace, baselineRecordKey)
	if err != nil {
		// Both a genuine cascade.KindNotFound (no baseline persisted yet)
		// and any other read error are treated identically here: "no
		// usable baseline" -> fail closed. Distinguishing them would not
		// change the outcome (§D-39 fail-closed applies to both a
		// missing AND an unverifiable baseline).
		return baselineRecordV1{}, "", false, nil
	}
	var rec baselineRecordV1
	if err := json.Unmarshal(data, &rec); err != nil {
		return baselineRecordV1{}, "", false, nil
	}
	warning := ""
	if rec.Version > 1 {
		warning = "baseline record schema_version is newer than this binary's v1 reader understands; reading tolerantly"
	}
	return rec, warning, true, nil
}
