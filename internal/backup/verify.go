// Purpose: S-42.T4's verification job -- probes one target's most recent
// snapshot via S-41.T4's real integrity gate (manifest signature +
// checksum verification, full object re-decrypt; read-only, never a
// restore), records the result in S-42.T1's per-target Outcome
// bookkeeping (the §22 VERIFIED state), and, on failure, files a real
// attention item via R/S-39.T1's queue so the failure is not merely
// detected but actually surfaced.
//
// Inputs: a TargetRecord, the collaborators BuildTarget needs (an
// *egress.Engine, a getenv func), a provider.Store namespace, an injected
// runtime.Clock, and an AttentionSink (nil disables the attention push --
// e.g. a caller that only wants the report/error and handles routing
// itself).
// Outputs: a VerificationReport plus a typed error on any refusal/failure.
// Constraints: never restores anything; never writes to the target beyond
// this package's existing read paths (ReadRepoConfig, VerifyIntegrity,
// ListSnapshots all already read-only). No bare time.Now/Sleep -- every
// instant comes from the injected Clock.
//
// SPORT: internal.backup.verify/ADD (P1-E19-W4-S42-T4).

// Package backup doc: see doc.go for the canonical package comment.
package backup

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"time"

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/fleet/supervision"
	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// OutcomeKindCreate/OutcomeKindVerify distinguish a create fire's Outcome
// (schedule.go's fireBackupTarget) from a verification fire's Outcome
// (this file's RunVerification) sharing policy.go's one Outcome shape. An
// empty Kind is an already-persisted S-42.T1 create Outcome that predates
// this distinction.
const (
	OutcomeKindCreate = "create"
	OutcomeKindVerify = "verify"
)

// OutcomeEffectiveKind returns o.Kind, or OutcomeKindCreate when o.Kind is
// empty (an Outcome persisted before S-42.T4 introduced Kind).
func (o Outcome) OutcomeEffectiveKind() string {
	if o.Kind == "" {
		return OutcomeKindCreate
	}
	return o.Kind
}

// AttentionSink is the minimal seam RunVerification needs to file a
// failure with R/S-39.T1's real attention queue: *supervision.Store's own
// Push method satisfies this directly (see var _ assertion in
// verify_test.go), so composition passes the real Store, never a
// package-local reimplementation of it.
type AttentionSink interface {
	Push(ctx context.Context, item supervision.AttentionItem) (supervision.AttentionItem, error)
}

// VerificationFailurePayload is the JSON shape this file encodes into the
// pushed attention item's SourceRef. attention.go documents SourceRef as
// "opaque to this package; interpreted by whichever subsystem pushed it" --
// this is backup's own interpretation, carrying exactly the ticket's
// {target, snapshot_id, failure_reason, journal_ref} structured payload
// the closed, four-field AttentionItem schema (frozen by R/S-39.T1) has no
// dedicated column for.
type VerificationFailurePayload struct {
	Target        string `json:"target"`
	SnapshotID    string `json:"snapshot_id"`
	FailureReason string `json:"failure_reason"`
	JournalRef    string `json:"journal_ref"`
}

// VerificationReport is RunVerification's result, returned to both the
// scheduled cron fire and the one-shot `backup verify` CLI/MCP surface
// (06 §5.8 automation parity: identical logic, identical shape).
type VerificationReport struct {
	Target        string     `json:"target"`
	Snapshot      SnapshotID `json:"snapshot"`
	Verified      bool       `json:"verified"`
	CheckedChunks int        `json:"checked_chunks"`
	ChainDepth    int        `json:"chain_depth"`
	DurationMS    int64      `json:"duration_ms"`
	FailureReason string     `json:"failure_reason,omitempty"`
}

// RunVerification builds target's real driver, resolves the repo's real
// manifest-verification pubkey, finds target's most recent snapshot, and
// calls S-41.T4's VerifyIntegrity -- the real read-only probe (chain-wide
// manifest signature/root_hash verification plus a full re-decrypt/hash
// check of the target snapshot's own objects; see integrity.go's own doc
// comment for the exact scope). On success it records a
// OutcomeKindVerify Outcome (the §22 VERIFIED state); on failure it
// records a failing OutcomeKindVerify Outcome AND, when sink is non-nil,
// pushes a real attention item so the failure is surfaced, not merely
// logged. A target with no snapshot yet is not a failure (nothing exists
// to verify): it returns a zero-value report and a nil error, recording
// no Outcome and pushing nothing.
func RunVerification(ctx context.Context, store provider.Store, namespace string, target TargetRecord, engine *egress.Engine, getenv func(string) string, clock runtime.Clock, sink AttentionSink) (VerificationReport, error) {
	when := clock.Now()
	report := VerificationReport{Target: target.Name}

	driver, err := BuildTarget(ctx, target, engine, getenv)
	if err != nil {
		return report, err
	}
	rows, err := ListSnapshots(ctx, store, namespace, []NamedTarget{{Name: target.Name, Target: driver}})
	if err != nil {
		return report, err
	}
	latest, ok := firstForTarget(rows, target.Name)
	if !ok {
		// Nothing to verify yet -- not a failure, no Outcome, no push.
		return report, nil
	}
	report.Snapshot = latest.ID

	cfg, err := ReadRepoConfig(ctx, driver)
	if err != nil {
		return failVerification(ctx, store, namespace, sink, when, report, err)
	}
	pubKey, err := decodeManifestPubKey(cfg.ManifestSigningPubKey)
	if err != nil {
		return failVerification(ctx, store, namespace, sink, when, report, err)
	}

	start := clock.Now()
	_, gate, verr := VerifyIntegrity(ctx, GateOptions{Target: driver, PubKey: pubKey}, latest.ID)
	elapsed := clock.Now().Sub(start)
	if verr != nil {
		return failVerification(ctx, store, namespace, sink, when, report, verr)
	}

	report.Verified = true
	report.CheckedChunks = gate.ObjectsVerified
	report.ChainDepth = gate.ChainDepth
	report.DurationMS = elapsed.Milliseconds()
	outcome := Outcome{
		Target: target.Name, Snapshot: string(latest.ID), When: when, Success: true,
		Kind: OutcomeKindVerify, CheckedChunks: report.CheckedChunks, DurationMS: report.DurationMS,
	}
	if err := RecordOutcome(ctx, store, namespace, outcome); err != nil {
		return report, err
	}
	return report, nil
}

// failVerification records the failing verification Outcome (regardless
// of RecordOutcome's own error, matching fireBackupTarget's precedent of
// never letting a bookkeeping failure hide the real one), pushes the
// attention item when sink is non-nil (regardless of the push's own
// error, for the identical reason), and returns (report, cause) -- cause
// is always the ORIGINAL failure, never RecordOutcome's or the sink's own
// error, so a caller's error-Kind check always sees the real refusal.
func failVerification(ctx context.Context, store provider.Store, namespace string, sink AttentionSink, when time.Time, report VerificationReport, cause error) (VerificationReport, error) {
	report.FailureReason = cause.Error()
	outcome := Outcome{
		Target: report.Target, Snapshot: string(report.Snapshot), When: when,
		Success: false, ErrorText: report.FailureReason, Kind: OutcomeKindVerify,
	}
	_ = RecordOutcome(ctx, store, namespace, outcome)
	if sink != nil {
		_ = pushVerificationFailure(ctx, sink, report.Target, report.Snapshot, report.FailureReason)
	}
	return report, cause
}

// pushVerificationFailure files one real attention item for a verification
// failure: Kind KindError (the closest of the four closed AttentionItem
// kinds to "a background check failed"), scoped globally under the target
// name (a backup target is not a session/task/project scope), SourceRef
// carrying the JSON-encoded VerificationFailurePayload. SourceRef keys
// Push's own idempotency on (Kind, SourceRef) -- since the payload embeds
// the failure reason, a NEW distinct failure on the same snapshot mints a
// new item rather than silently deduping against a stale one, at the cost
// of not deduping two identical repeated failures on an unpolled snapshot;
// recorded here, not papered over.
func pushVerificationFailure(ctx context.Context, sink AttentionSink, target string, snapshot SnapshotID, reason string) error {
	payload := VerificationFailurePayload{
		Target: target, SnapshotID: string(snapshot), FailureReason: reason,
		JournalRef: "cascade backup list --target " + target,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "backup: encode verification failure payload")
	}
	item := supervision.AttentionItem{
		Kind:      supervision.KindError,
		SourceRef: string(raw),
		ScopeRef:  supervision.ScopeRef{Kind: scope.ScopeKindGlobal, ID: target},
	}
	_, err = sink.Push(ctx, item)
	return err
}

// firstForTarget returns rows' first entry naming target (ListSnapshots
// already sorts rows newest-first), or ok == false when target has no
// snapshot.
func firstForTarget(rows []SnapshotSummary, target string) (SnapshotSummary, bool) {
	for _, row := range rows {
		if row.Target == target {
			return row, true
		}
	}
	return SnapshotSummary{}, false
}

// decodeManifestPubKey decodes a RepoConfig.ManifestSigningPubKey (base64
// Ed25519, validated non-empty by config.go's validateManifestSigningPubKey
// on every write) into the ed25519.PublicKey VerifyIntegrity needs. Fails
// closed on an empty or malformed value -- config.go allows an empty
// pubkey only before any snapshot has ever been created, and verification
// has nothing to check in that state either way.
func decodeManifestPubKey(encoded string) (ed25519.PublicKey, error) {
	if encoded == "" {
		return nil, cascade.New(cascade.KindNotFound, "backup: target has no manifest signing pubkey yet (no snapshot created)")
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, cascade.New(cascade.KindIntegrity, "backup: repository manifest public key is invalid")
	}
	return ed25519.PublicKey(raw), nil
}
