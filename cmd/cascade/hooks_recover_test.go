//go:build !windows

// Purpose: recoverEvidenceEffects (hooks.go), the start-up step that settles
// evidence audit intents a crashed run left open. One intent has its row (the
// crash fell between the row's commit and the confirmation) and one has none
// (the crash fell before the row); recovery confirms the first and fails the
// second, and a second pass finds nothing pending.
//
// SPORT: cmd/cascade/daemon:hooks (TEST, P1-SEC-21).
package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// noConfirm is an EffectWriter whose ConfirmEffect fails, standing in for a
// process that died after the evidence row committed.
type noConfirm struct{ audit.EffectWriter }

func (noConfirm) ConfirmEffect(context.Context, audit.EffectHandle, string) (audit.Record, error) {
	return audit.Record{}, cascade.New(cascade.KindUnavailable, "process died before the confirmation")
}

// newRecoverJobsStore opens a fresh cascade.db with job-1 and exec-1 seeded.
func newRecoverJobsStore(t *testing.T, clock runtime.Clock) *jobs.Store {
	t.Helper()
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "cascade.db"))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	if err := jobs.ApplyJobsSchema(ctx, db, migrate.SQLiteEmitter{}, clock, "", ""); err != nil {
		t.Fatalf("ApplyJobsSchema: %v", err)
	}
	if err := jobs.ApplyEvidenceSchema(ctx, db, migrate.SQLiteEmitter{}, clock, "", ""); err != nil {
		t.Fatalf("ApplyEvidenceSchema: %v", err)
	}
	jstore := jobs.NewStore(db)
	if err := jstore.PutJob(ctx, jobs.Job{
		ID: "job-1", State: jobs.JobStatePending, CreatedAt: 1, UpdatedAt: 1, Capabilities: []string{"code"},
		MutableScope: "repo:/x", RiskClass: "normal", MinTaskClass: "code", NodeRequirements: "{}",
		TimeoutSeconds: 60, CostCeiling: 1, Priority: 1, ConsequenceClass: jobs.ConsequenceNormal, DataClass: new(provider.SensitivityInternal),
	}); err != nil {
		t.Fatalf("PutJob: %v", err)
	}
	if err := jstore.PutExecution(ctx, jobs.Execution{ID: "exec-1", JobID: "job-1", State: jobs.ExecutionRunning}); err != nil {
		t.Fatalf("PutExecution: %v", err)
	}
	return jstore
}

func TestRecoverEvidenceEffects_ConfirmsCommittedRowAndFailsMissingRow(t *testing.T) {
	ctx := context.Background()
	clock := runtime.NewFixedClock(time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC))
	jstore := newRecoverJobsStore(t, clock)
	effects, err := audit.NewEffectLog(audit.New(storetest.NewMemStore(), clock, nil))
	if err != nil {
		t.Fatalf("NewEffectLog: %v", err)
	}
	authz := jobs.NewProducerAuthz(jstore, func() bool { return true }, nil)
	crashed, err := jobs.NewEvidenceLedger(jstore, clock, noConfirm{effects}, authz)
	if err != nil {
		t.Fatalf("NewEvidenceLedger: %v", err)
	}
	rec := jobs.EvidenceRecord{
		JobID: "job-1", Kind: jobs.EvidenceBuild, ProducerCapability: jobs.ProducerControllerRun,
		AttestorIdentity: "daemon:d-1", Outcome: jobs.OutcomePass, IdempotencyKey: "idem-1",
	}
	if _, err := crashed.Append(ctx, rec, jobs.AppendAuthorization{ExecutionID: "exec-1"}); err == nil {
		t.Fatal("Append with a dead confirmation = nil error")
	}
	// A second intent whose row never committed.
	const orphanKey = "jobs.evidence:job-1:idem-2"
	if _, err := effects.BeginEffect(ctx, audit.EffectRequest{Kind: audit.KindPolicyDecide, Actor: "jobs.evidence",
		Action: "evidence.append:build", Key: orphanKey}); err != nil {
		t.Fatalf("BeginEffect: %v", err)
	}

	ledger, err := jobs.NewEvidenceLedger(jstore, clock, effects, authz)
	if err != nil {
		t.Fatalf("NewEvidenceLedger: %v", err)
	}
	if err := recoverEvidenceEffects(ctx, effects, ledger); err != nil {
		t.Fatalf("recoverEvidenceEffects: %v", err)
	}
	for key, want := range map[string]audit.EffectPhase{
		"jobs.evidence:job-1:idem-1": audit.EffectConfirmed,
		orphanKey:                    audit.EffectFailed,
	} {
		if got, found, err := effects.EffectState(ctx, key); err != nil || !found || got != want {
			t.Fatalf("EffectState(%s) = %q, %v, %v; want %q", key, got, found, err, want)
		}
	}
	if pending, err := effects.PendingEffects(ctx); err != nil || len(pending) != 0 {
		t.Fatalf("PendingEffects after recovery = %+v, %v; want none", pending, err)
	}
	if err := recoverEvidenceEffects(ctx, effects, ledger); err != nil {
		t.Fatalf("second recoverEvidenceEffects: %v", err)
	}
}

// TestWireCompletionHookPack_SettlesOpenEvidenceIntentsAtStartup drives the
// recovery through the real wiring: an intent left open by a crashed run,
// with no evidence row behind it, is Failed once the pack is wired.
func TestWireCompletionHookPack_SettlesOpenEvidenceIntentsAtStartup(t *testing.T) {
	ctx := context.Background()
	paths := fakeDaemonPaths{root: t.TempDir()}
	if err := os.MkdirAll(paths.DataDir(), 0o700); err != nil {
		t.Fatalf("MkdirAll(DataDir): %v", err)
	}
	clock := runtime.NewFixedClock(time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC))
	kv := storetest.NewMemStore()
	seed, err := audit.NewEffectLog(audit.New(kv, clock, nil))
	if err != nil {
		t.Fatalf("NewEffectLog: %v", err)
	}
	const key = "jobs.evidence:job-9:idem-9"
	if _, err := seed.BeginEffect(ctx, audit.EffectRequest{Kind: audit.KindPolicyDecide, Actor: "jobs.evidence",
		Action: "evidence.append:build", Key: key}); err != nil {
		t.Fatalf("BeginEffect: %v", err)
	}
	if err := wireCompletionHookPack(ctx, rpc.NewRegistry(), kv, clock, events.New(kv, clock), paths); err != nil {
		t.Fatalf("wireCompletionHookPack: %v", err)
	}
	check, err := audit.NewEffectLog(audit.New(kv, clock, nil))
	if err != nil {
		t.Fatalf("NewEffectLog: %v", err)
	}
	if got, found, err := check.EffectState(ctx, key); err != nil || !found || got != audit.EffectFailed {
		t.Fatalf("EffectState(%s) after wiring = %q, %v, %v; want failed", key, got, found, err)
	}
}
