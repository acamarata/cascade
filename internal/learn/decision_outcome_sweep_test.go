package learn

// Purpose: tests for ReconcileTerminalOutcomes and SweepOrphanedPending.
// SPORT: internal.learn.ReconcileTerminalOutcomes/TESTED,
//   internal.learn.SweepOrphanedPending/TESTED (P1-TOP-10).

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

// seedSweepDecision records a decision with its own job and execution id.
func seedSweepDecision(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	d := testDecision(id)
	d.JobID, d.ExecutionID = "job-"+id, "exec-"+id
	if err := (SQLiteSchedulerDecisionWriter{}).Record(context.Background(), db, d); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
}

// outcomeOf returns the stored outcome kind of id, or "" when pending.
func outcomeOf(t *testing.T, db *sql.DB, id string) string {
	t.Helper()
	var k string
	err := db.QueryRow(`SELECT outcome_kind FROM `+tableDecisionOutcome+` WHERE decision_id = ?`, id).Scan(&k)
	if errors.Is(err, sql.ErrNoRows) {
		return ""
	}
	if err != nil {
		t.Fatalf("read outcome %s: %v", id, err)
	}
	return k
}

// TestSweepOrphanedPendingExpires: every orphaned pending decision is expired,
// a repeat inserts nothing, and a live decision is untouched.
func TestSweepOrphanedPendingExpires(t *testing.T) {
	ctx := context.Background()
	db := openMigratedDB(t, tmplOutcome)
	for _, id := range []string{"o1", "o2", "o3", "live"} {
		seedSweepDecision(t, db, id)
	}
	live := func(exec string) (bool, error) { return exec == "exec-live", nil }
	n, err := SweepOrphanedPending(ctx, db, newTestClock(), live)
	if err != nil || n != 3 {
		t.Fatalf("first sweep = %d, %v; want 3, nil", n, err)
	}
	for _, id := range []string{"o1", "o2", "o3"} {
		if got := outcomeOf(t, db, id); got != string(OutcomeKindExpired) {
			t.Errorf("%s outcome = %q, want expired", id, got)
		}
	}
	if got := outcomeOf(t, db, "live"); got != "" {
		t.Errorf("live decision outcome = %q, want none", got)
	}
	if n, err := SweepOrphanedPending(ctx, db, newTestClock(), live); err != nil || n != 0 {
		t.Fatalf("second sweep = %d, %v; want 0, nil", n, err)
	}
}

// TestSweepLivenessErrorWritesNothing: a liveness error is returned, not read
// as absence.
func TestSweepLivenessErrorWritesNothing(t *testing.T) {
	db := openMigratedDB(t, tmplOutcome)
	seedSweepDecision(t, db, "e1")
	boom := errors.New("ledger unreadable")
	n, err := SweepOrphanedPending(context.Background(), db, newTestClock(), func(string) (bool, error) { return false, boom })
	if !errors.Is(err, boom) || n != 0 || outcomeOf(t, db, "e1") != "" {
		t.Fatalf("sweep = %d, %v, outcome %q; want 0, boom, none", n, err, outcomeOf(t, db, "e1"))
	}
}

// TestReconcileTerminalOutcomesBeforeSweep: a terminal job with no outcome row
// gets its mapped row and is not expired by the next sweep; a repeat changes
// zero rows; a state read error is returned and invents nothing.
func TestReconcileTerminalOutcomesBeforeSweep(t *testing.T) {
	ctx := context.Background()
	db := openMigratedDB(t, tmplOutcome)
	for _, id := range []string{"a", "r", "c", "p"} {
		seedSweepDecision(t, db, id)
	}
	boom := errors.New("state unreadable")
	if n, err := ReconcileTerminalOutcomes(ctx, db, newTestClock(), func(context.Context) ([]TerminalJob, error) { return nil, boom }); !errors.Is(err, boom) || n != 0 {
		t.Fatalf("erroring reconcile = %d, %v; want 0, boom", n, err)
	}
	if got := outcomeOf(t, db, "a"); got != "" {
		t.Fatalf("outcome invented on a read error: %q", got)
	}
	terminal := func(context.Context) ([]TerminalJob, error) {
		return []TerminalJob{{"job-a", "accepted"}, {"job-r", "rejected"}, {"job-c", "cancelled"}}, nil
	}
	if n, err := ReconcileTerminalOutcomes(ctx, db, newTestClock(), terminal); err != nil || n != 3 {
		t.Fatalf("reconcile = %d, %v; want 3, nil", n, err)
	}
	for id, want := range map[string]string{"a": "completed", "r": "failed", "c": "cancelled", "p": ""} {
		if got := outcomeOf(t, db, id); got != want {
			t.Errorf("%s outcome = %q, want %q", id, got, want)
		}
	}
	if n, err := ReconcileTerminalOutcomes(ctx, db, newTestClock(), terminal); err != nil || n != 0 {
		t.Fatalf("repeat reconcile = %d, %v; want 0, nil", n, err)
	}
	if n, err := SweepOrphanedPending(ctx, db, newTestClock(), func(string) (bool, error) { return false, nil }); err != nil || n != 1 {
		t.Fatalf("sweep = %d, %v; want 1 (only p)", n, err)
	}
	if got := outcomeOf(t, db, "a"); got != "completed" {
		t.Errorf("reconciled outcome rewritten to %q", got)
	}
}

// TestReconcileUnknownStateWritesNothing: an unknown state is refused whole.
func TestReconcileUnknownStateWritesNothing(t *testing.T) {
	db := openMigratedDB(t, tmplOutcome)
	seedSweepDecision(t, db, "u")
	seedSweepDecision(t, db, "v")
	n, err := ReconcileTerminalOutcomes(context.Background(), db, newTestClock(), func(context.Context) ([]TerminalJob, error) {
		return []TerminalJob{{"job-u", "accepted"}, {"job-v", "weird"}}, nil
	})
	if err == nil || n != 0 || outcomeOf(t, db, "u") != "" {
		t.Fatalf("reconcile = %d, %v, u outcome %q; want 0, error, none", n, err, outcomeOf(t, db, "u"))
	}
}
