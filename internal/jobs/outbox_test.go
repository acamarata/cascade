package jobs

// Purpose: RecordIntent/MarkEffect/ConfirmEffect's transactional
//
//	behavior, the derived stable idempotency key, duplicate-insert
//	idempotency, and Reconcile's three outcomes (confirm-without-
//	reperform, reperform, and terminal-state compensation) -- all
//	against a REAL modernc-sqlite db (Art.2).
//
// SPORT: jobs/scheduler-outbox/ADD (tests) (P1-E29-W6-S59-T5).

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
)

// newOutboxTestStore applies both the jobs and jobs-outbox MigrationSets
// against one fresh real db, and seeds one base job row so outbox rows'
// job_id foreign key resolves.
func newOutboxTestStore(t *testing.T, jobID string) *Store {
	t.Helper()
	store := newTestStore(t)
	if err := ApplyOutboxSchema(context.Background(), store.db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		t.Fatalf("ApplyOutboxSchema: %v", err)
	}
	j := baseJob(jobID)
	if err := store.PutJob(context.Background(), j); err != nil {
		t.Fatalf("seed job: %v", err)
	}
	return store
}

func TestDeriveIdempotencyKey_Stable(t *testing.T) {
	k1 := DeriveIdempotencyKey(OutboxSiteSpawn, "job-1", 1, "hash-a")
	k2 := DeriveIdempotencyKey(OutboxSiteSpawn, "job-1", 1, "hash-a")
	if k1 != k2 {
		t.Fatalf("DeriveIdempotencyKey not stable: %q != %q", k1, k2)
	}
	k3 := DeriveIdempotencyKey(OutboxSiteSpawn, "job-1", 2, "hash-a")
	if k1 == k3 {
		t.Fatalf("DeriveIdempotencyKey ignored attempt_generation: %q == %q", k1, k3)
	}
}

func TestRecordIntent_DuplicateIsIdempotentNoOp(t *testing.T) {
	store := newOutboxTestStore(t, "job-1")
	ctx := context.Background()
	intent := OutboxIntent{JobID: "job-1", Site: OutboxSiteSpawn, AttemptGeneration: 1, PayloadHash: "h1"}

	var id1, id2 string
	if err := store.withTx(ctx, func(tx *sql.Tx) (err error) { id1, err = RecordIntent(ctx, tx, 100, intent); return }); err != nil {
		t.Fatalf("RecordIntent #1: %v", err)
	}
	if err := store.withTx(ctx, func(tx *sql.Tx) (err error) { id2, err = RecordIntent(ctx, tx, 200, intent); return }); err != nil {
		t.Fatalf("RecordIntent #2 (duplicate): %v", err)
	}
	if id1 != id2 {
		t.Fatalf("duplicate RecordIntent minted a second row: %q != %q", id1, id2)
	}
}

func TestRecordIntent_InvalidSiteRefused(t *testing.T) {
	store := newOutboxTestStore(t, "job-1")
	ctx := context.Background()
	err := store.withTx(ctx, func(tx *sql.Tx) error {
		_, err := RecordIntent(ctx, tx, 100, OutboxIntent{JobID: "job-1", Site: "not-a-real-site"})
		return err
	})
	if err != ErrInvalidOutboxSite {
		t.Fatalf("RecordIntent(invalid site) = %v, want ErrInvalidOutboxSite", err)
	}
}

func TestReconcile_ConfirmsWithoutReperformingWhenEffectExists(t *testing.T) {
	store := newOutboxTestStore(t, "job-1")
	ctx := context.Background()
	key := seedOutboxRow(t, store, "job-1", OutboxSiteSpawn, OutboxEffectState)

	sched := NewScheduler(runtime.NewFixedClock(time.Unix(2000, 0)))
	var reperformed int
	probes := map[OutboxSite]SiteProbe{OutboxSiteSpawn: func(context.Context, OutboxRow) (bool, error) { return true, nil }}
	compensate := map[OutboxSite]CompensateFn{OutboxSiteSpawn: func(context.Context, OutboxRow) error { reperformed++; return nil }}

	result, err := sched.reconcileOutbox(ctx, store, probes, compensate, 2000)
	if err != nil {
		t.Fatalf("reconcileOutbox: %v", err)
	}
	if len(result.Confirmed) != 1 || len(result.ToReperform) != 0 {
		t.Fatalf("result = %#v, want exactly one confirmed row, none re-performed", result)
	}
	if reperformed != 0 {
		t.Fatal("compensate/reperform side effect ran despite the effect already existing")
	}
	assertOutboxState(t, store, key, OutboxConfirmed)

	// Exactly-once: a second reconcile pass sees no unconfirmed rows left.
	result2, err := sched.reconcileOutbox(ctx, store, probes, compensate, 2001)
	if err != nil {
		t.Fatalf("second reconcileOutbox: %v", err)
	}
	if len(result2.Confirmed)+len(result2.ToReperform)+len(result2.Compensated) != 0 {
		t.Fatalf("second reconcile pass found more work: %#v, want none (exactly-once)", result2)
	}
}

func TestReconcile_ReperformsWhenEffectAbsent(t *testing.T) {
	store := newOutboxTestStore(t, "job-1")
	ctx := context.Background()
	seedOutboxRow(t, store, "job-1", OutboxSiteLeaseAcquire, OutboxIntentState)

	sched := NewScheduler(runtime.NewFixedClock(time.Unix(2000, 0)))
	probes := map[OutboxSite]SiteProbe{OutboxSiteLeaseAcquire: func(context.Context, OutboxRow) (bool, error) { return false, nil }}

	result, err := sched.reconcileOutbox(ctx, store, probes, nil, 2000)
	if err != nil {
		t.Fatalf("reconcileOutbox: %v", err)
	}
	if len(result.ToReperform) != 1 || len(result.Confirmed) != 0 {
		t.Fatalf("result = %#v, want exactly one row marked to re-perform", result)
	}
}

func TestReconcile_CompensatesTerminalJobInsteadOfAdvancing(t *testing.T) {
	store := newOutboxTestStore(t, "job-1")
	ctx := context.Background()
	key := seedOutboxRow(t, store, "job-1", OutboxSiteCIDispatch, OutboxEffectState)
	if err := store.PutTransition(ctx, "job-1", JobStateFailed, 1000); err != nil {
		t.Fatalf("PutTransition to terminal: %v", err)
	}

	sched := NewScheduler(runtime.NewFixedClock(time.Unix(2000, 0)))
	var compensated int
	probes := map[OutboxSite]SiteProbe{OutboxSiteCIDispatch: func(context.Context, OutboxRow) (bool, error) {
		t.Fatal("probe called for a terminal job's row: compensation must take precedence")
		return false, nil
	}}
	compensate := map[OutboxSite]CompensateFn{OutboxSiteCIDispatch: func(context.Context, OutboxRow) error { compensated++; return nil }}

	result, err := sched.reconcileOutbox(ctx, store, probes, compensate, 2000)
	if err != nil {
		t.Fatalf("reconcileOutbox: %v", err)
	}
	if len(result.Compensated) != 1 || compensated != 1 {
		t.Fatalf("result = %#v, compensated calls = %d, want exactly one compensation", result, compensated)
	}
	assertOutboxState(t, store, key, OutboxReconciled)
}

// seedOutboxRow inserts one outbox row directly at the given state
// (bypassing RecordIntent's intent-only insert) for reconcile tests that
// need to start from `effect`.
func seedOutboxRow(t *testing.T, store *Store, jobID string, site OutboxSite, state OutboxState) string {
	t.Helper()
	ctx := context.Background()
	var key string
	err := store.withTx(ctx, func(tx *sql.Tx) error {
		intent := OutboxIntent{JobID: jobID, Site: site, AttemptGeneration: 1, PayloadHash: "h"}
		if _, err := RecordIntent(ctx, tx, 1000, intent); err != nil {
			return err
		}
		key = DeriveIdempotencyKey(site, jobID, 1, "h")
		if state == OutboxEffectState {
			return MarkEffect(ctx, tx, 1000, key)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seedOutboxRow: %v", err)
	}
	return key
}

func assertOutboxState(t *testing.T, store *Store, key string, want OutboxState) {
	t.Helper()
	var row OutboxRow
	err := store.withTx(context.Background(), func(tx *sql.Tx) error {
		r, ok, err := getOutboxByKeyTx(context.Background(), tx, key)
		if err != nil || !ok {
			t.Fatalf("getOutboxByKeyTx: row %v, ok %v, err %v", r, ok, err)
		}
		row = r
		return nil
	})
	if err != nil {
		t.Fatalf("assertOutboxState: %v", err)
	}
	if row.State != want {
		t.Fatalf("outbox row state = %q, want %q", row.State, want)
	}
}

func TestApplyOutboxSchema_NilGuards(t *testing.T) {
	if err := ApplyOutboxSchema(context.Background(), nil, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err == nil {
		t.Fatal("ApplyOutboxSchema(nil db) = nil, want an error")
	}
	db := openTestDB(t)
	if err := ApplyOutboxSchema(context.Background(), db, migrate.SQLiteEmitter{}, nil, "", ""); err == nil {
		t.Fatal("ApplyOutboxSchema(nil clock) = nil, want an error")
	}
}

func TestRecordIntent_RequiresJobID(t *testing.T) {
	store := newOutboxTestStore(t, "job-1")
	err := store.withTx(context.Background(), func(tx *sql.Tx) error {
		_, err := RecordIntent(context.Background(), tx, 100, OutboxIntent{Site: OutboxSiteSpawn})
		return err
	})
	if err == nil {
		t.Fatal("RecordIntent(no job id) = nil, want an error")
	}
}

func TestConfirmEffect_NoRowForKeyIsNotFound(t *testing.T) {
	store := newOutboxTestStore(t, "job-1")
	err := store.withTx(context.Background(), func(tx *sql.Tx) error {
		return ConfirmEffect(context.Background(), tx, 100, "no-such-key")
	})
	if err == nil {
		t.Fatal("ConfirmEffect(unknown key) = nil, want KindNotFound")
	}
}
