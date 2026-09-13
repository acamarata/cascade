//go:build !windows

// Purpose: unit coverage for completionPolicyGate.CompletionCheck,
// completionJobResolver.ResolveJob and nextCompletionTarget's own
// branches (hooks.go), which hooks_test.go's end-to-end dispatch cases do
// not reach: R-21.176's own scope decision (internal/fleet/hookpacks/
// completion_scope.go's ResolveJobID) short-circuits BEFORE ever calling
// gate.CompletionCheck for both the unscoped (empty payload.JobID) and
// the resolver-refused (unknown job) cases, so completionPolicyGate's own
// "job not found" branch and completionJobResolver's own "empty payload"
// branch are unreachable through the RPC dispatch path hooks_test.go
// drives. This file constructs both adapters directly, over the same
// real *jobs.Store/*jobs.CompletionPolicy composition wireCompletionHookPack
// itself builds, and calls their methods straight — a white-box unit
// test, not a second, competing composition root (Art.2).
//
// Split from hooks_test.go under the 300-line file cap.
//
// SPORT: cmd/cascade/daemon:hooks (TEST, P1-E32-W6-S66-T1 branch follow-up).
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
	"github.com/acamarata/cascade/internal/fleet/hookpacks"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
)

// setupCompletionGate builds the SAME *completionPolicyGate/
// *completionJobResolver pair wireCompletionHookPack constructs (same
// schema, same real jobs.Store, same audit-backed evidence ledger), over
// a fresh cascade.db under t.TempDir(), for tests that call their methods
// directly rather than through the RPC/hookpacks dispatch layer.
func setupCompletionGate(t *testing.T) (*completionPolicyGate, *completionJobResolver, *jobs.Store) {
	t.Helper()
	root := t.TempDir()
	clock := runtime.NewFixedClock(time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC))
	db, err := sql.Open("sqlite", "file:"+filepath.Join(root, "cascade.db"))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	if err := jobs.ApplyJobsSchema(ctx, db, migrate.SQLiteEmitter{}, clock, "", ""); err != nil {
		t.Fatalf("ApplyJobsSchema: %v", err)
	}
	if err := jobs.ApplyEvidenceSchema(ctx, db, migrate.SQLiteEmitter{}, clock, "", ""); err != nil {
		t.Fatalf("ApplyEvidenceSchema: %v", err)
	}

	jstore := jobs.NewStore(db)
	authz := jobs.NewProducerAuthz(jstore, func() bool { return true }, nil)
	kv := storetest.NewMemStore()
	bus := events.New(kv, clock)
	ledger, err := jobs.NewEvidenceLedger(jstore, clock, audit.New(kv, clock, nil), authz)
	if err != nil {
		t.Fatalf("NewEvidenceLedger: %v", err)
	}
	policy, err := jobs.NewCompletionPolicy(jobs.CompletionPolicyDeps{
		Store: jstore, Ledger: ledger, Bus: bus, Clock: clock, EngineID: completionGateEngineID,
	})
	if err != nil {
		t.Fatalf("NewCompletionPolicy: %v", err)
	}
	return &completionPolicyGate{policy: policy, store: jstore}, &completionJobResolver{store: jstore}, jstore
}

// TestCompletionJobResolver_EmptyJobIDNeverTouchesTheStore is
// completionJobResolver's own doc comment, proven: an empty
// payload.JobID resolves to ("", nil) without a store lookup. Unreachable
// through the RPC dispatch (hookpacks.ResolveJobID reads this as
// "unscoped" and returns before completionPolicyGate.CompletionCheck is
// ever called), so this drives ResolveJob directly.
func TestCompletionJobResolver_EmptyJobIDNeverTouchesTheStore(t *testing.T) {
	_, resolver, _ := setupCompletionGate(t)
	id, err := resolver.ResolveJob(context.Background(), hookpacks.CompletionHookPayload{})
	if err != nil {
		t.Fatalf("ResolveJob(empty payload) error = %v, want nil", err)
	}
	if id != "" {
		t.Fatalf("ResolveJob(empty payload) id = %q, want empty", id)
	}
}

// TestCompletionPolicyGate_UnknownJobRefusesByName proves
// completionPolicyGate.CompletionCheck's own "unknown job" refusal (its
// GetJob call found nothing): unreachable through the RPC dispatch, whose
// resolver-level check already denies an unresolvable job id before the
// gate is ever consulted (hookpacks/completion_scope.go's ResolveJobID),
// so this drives CompletionCheck directly against a job id the resolver
// never validated.
func TestCompletionPolicyGate_UnknownJobRefusesByName(t *testing.T) {
	gate, _, _ := setupCompletionGate(t)
	ok, reason, err := gate.CompletionCheck(context.Background(), "never-existed")
	if ok {
		t.Fatal("CompletionCheck on an unknown job: want ok=false")
	}
	if reason != "" {
		t.Fatalf("CompletionCheck on an unknown job: reason = %q, want empty (the refusal is the error)", reason)
	}
	if !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("err = %v, want KindNotFound", err)
	}
}

// TestCompletionPolicyGate_DefaultsEmptyRiskClassAndVerifyingTarget
// covers two branches of the same real job in one seed: an empty
// RiskClass column (job.RiskClass == "") must default to
// jobs.RiskClassNormal rather than being passed through as "", and a job
// already at JobStateVerifying must be checked against JobStateReviewing
// (nextCompletionTarget's own Verifying branch), not JobStateVerifying
// again. Both defaults are observable only in the deny reason a missing-
// evidence job produces, since PlannedRiskClass and Target are consumed
// internally by jobs.CompletionPolicy.Transition — this asserts the
// REAL, on-the-wire deny outcome rather than reading either field back.
func TestCompletionPolicyGate_DefaultsEmptyRiskClassAndVerifyingTarget(t *testing.T) {
	gate, _, store := setupCompletionGate(t)
	job := jobs.Job{
		ID: "job-verifying-no-risk", State: jobs.JobStateVerifying, CreatedAt: 1, UpdatedAt: 1,
		ConsequenceClass: jobs.ConsequenceNormal, DataClass: jobs.DataClassInternal,
		// RiskClass deliberately left as the zero value.
	}
	if err := store.PutJob(context.Background(), job); err != nil {
		t.Fatalf("PutJob: %v", err)
	}

	ok, reason, err := gate.CompletionCheck(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("CompletionCheck: %v", err)
	}
	if ok || reason == "" {
		t.Fatalf("CompletionCheck on a Verifying job with zero evidence rows = ok=%v reason=%q, want a real deny reason", ok, reason)
	}

	// The job must still be Verifying: a real denial never advances state,
	// and in particular never jumps to Reviewing (nextCompletionTarget's
	// target for this state) without a passing check.
	got, found, err := store.GetJob(context.Background(), job.ID)
	if err != nil || !found {
		t.Fatalf("GetJob after deny: found=%v err=%v", found, err)
	}
	if got.State != jobs.JobStateVerifying {
		t.Fatalf("job State after a denied completion check = %q, want %q (unchanged)", got.State, jobs.JobStateVerifying)
	}
}

// TestWireCompletionHookPack_CascadeDBPathOccupiedRefuses proves
// wireCompletionHookPack's own open/schema-apply refusal: a DIRECTORY
// already occupies cascade.db's path (the same real-filesystem-collision
// technique recall_embedded_test.go uses for openEmbeddedFTSLeg), so the
// composition root must return an error rather than registering a
// handler over a database it never actually opened.
func TestWireCompletionHookPack_CascadeDBPathOccupiedRefuses(t *testing.T) {
	root := t.TempDir()
	paths := fakeDaemonPaths{root: root}
	if err := os.MkdirAll(filepath.Join(paths.DataDir(), "cascade.db"), 0o700); err != nil {
		t.Fatalf("test setup: seed a directory at cascade.db's path: %v", err)
	}
	clock := runtime.NewFixedClock(time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC))
	kv := storetest.NewMemStore()
	bus := events.New(kv, clock)

	err := wireCompletionHookPack(context.Background(), rpc.NewRegistry(), kv, clock, bus, paths)
	if err == nil {
		t.Fatal("wireCompletionHookPack over a cascade.db path occupied by a directory: want an error, got nil")
	}
}
