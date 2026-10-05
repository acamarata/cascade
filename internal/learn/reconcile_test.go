// Purpose: OutcomeReconciler tests -- one outcome row per terminal job
//
//	across all four terminal states, and a second pass writing nothing.
//
// SPORT: learn/reconcile/ADD (P1-E31-W6-S64-T1).
package learn

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/jobs"
)

// baseJobForReconcile returns a minimally-valid terminal jobs.Job, mirroring
// internal/jobs' own baseJob test helper (a different package's own test
// file, duplicated by convention rather than imported -- jobs' baseJob is
// unexported).
func baseJobForReconcile(id string) jobs.Job {
	return jobs.Job{
		ID: id, State: jobs.JobStatePending, CreatedAt: 1, UpdatedAt: 6,
		Capabilities: []string{"code"}, MutableScope: "repo:/tmp/x", RiskClass: "normal",
		MinTaskClass: "code", NodeRequirements: "{}", TimeoutSeconds: 60, CostCeiling: 1.0,
		Priority: 1, ConsequenceClass: jobs.ConsequenceNormal, DataClass: jobs.DataClassInternal,
	}
}

func newTestJobsStore(t *testing.T) *jobs.Store {
	t.Helper()
	return jobs.NewStore(openMigratedDB(t, tmplJobs))
}

// recordingWriter counts Record calls per job_id, so a second Reconcile
// pass can be asserted to write nothing new.
type recordingWriter struct{ seen map[string]int }

func (w *recordingWriter) Record(_ context.Context, o TelemetryOutcome) error {
	if w.seen == nil {
		w.seen = map[string]int{}
	}
	w.seen[o.JobID]++
	return nil
}

// TestOutcomeReconcilerRecordsEachTerminalJobOnce covers all four terminal
// states plus a second Reconcile pass that adds nothing.
func TestOutcomeReconcilerRecordsEachTerminalJobOnce(t *testing.T) {
	store := newTestJobsStore(t)
	ctx := context.Background()
	states := []jobs.JobState{jobs.JobStateAccepted, jobs.JobStateRejected, jobs.JobStateCancelled, jobs.JobStateFailed}
	for i, state := range states {
		j := baseJobForReconcile(string(states[i]) + "-job")
		j.State = state
		if err := store.PutJob(ctx, j); err != nil {
			t.Fatalf("PutJob(%s): %v", state, err)
		}
	}
	w := &recordingWriter{}
	rec := OutcomeReconciler{Store: store, Writer: w}
	if err := rec.Reconcile(ctx); err != nil {
		t.Fatalf("first Reconcile: %v", err)
	}
	if len(w.seen) != len(states) {
		t.Fatalf("recorded %d jobs, want %d: %v", len(w.seen), len(states), w.seen)
	}
	for _, state := range states {
		id := string(state) + "-job"
		if w.seen[id] != 1 {
			t.Errorf("job %s recorded %d times, want 1", id, w.seen[id])
		}
	}
	if err := rec.Reconcile(ctx); err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	for _, state := range states {
		id := string(state) + "-job"
		if w.seen[id] != 2 {
			t.Errorf("job %s: recordingWriter always re-records (no idempotency at this layer -- the REAL "+
				"idempotency is SQLiteOutcomeWriter's own ON CONFLICT DO NOTHING, covered by "+
				"TestTelemetryOutcomeRoundTrip's writer, not this double); got %d calls", id, w.seen[id])
		}
	}
}

// TestOutcomeReconcilerRealWriterIdempotent proves idempotency end-to-end
// against the REAL SQLiteOutcomeWriter: a second Reconcile pass inserts
// zero additional rows.
func TestOutcomeReconcilerRealWriterIdempotent(t *testing.T) {
	ctx := context.Background()
	j := baseJobForReconcile("job-idem-1")
	j.State = jobs.JobStateAccepted
	// One db carrying both the jobs schema and the learn schema, so the
	// SAME connection backs both the Store the reconciler lists from and
	// the SQLiteOutcomeWriter it records through (the learn, usage and
	// jobs schemas, migrated once and copied).
	db := openMigratedDB(t, tmplOutcomeJobs)
	store := jobs.NewStore(db)
	if err := store.PutJob(ctx, j); err != nil {
		t.Fatalf("PutJob: %v", err)
	}
	w := NewSQLiteOutcomeWriter(db, newTestClock())
	rec := OutcomeReconciler{Store: store, Writer: w}
	if err := rec.Reconcile(ctx); err != nil {
		t.Fatalf("first Reconcile: %v", err)
	}
	if err := rec.Reconcile(ctx); err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+tableTelemetryOutcomes+` WHERE job_id = ?`, j.ID).Scan(&count); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if count != 1 {
		t.Errorf("got %d jobs_telemetry_outcomes rows for %s after two Reconcile passes, want 1", count, j.ID)
	}
}
