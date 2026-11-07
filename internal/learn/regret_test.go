package learn

// Purpose: tests for the write-once RegretFiller and the completed/failed-only
//   regret aggregate.
// SPORT: internal.learn.RegretFiller/TESTED, internal.learn.RegretAggregate/TESTED (P1-TOP-10C).

import (
	"context"
	"database/sql"
	"testing"
)

// seedRegretDecision records decision id for job, its economics (utility u)
// and, when kind is not "", its outcome.
func seedRegretDecision(t *testing.T, db *sql.DB, id, job string, u float64, kind OutcomeKind) {
	t.Helper()
	ctx := context.Background()
	d := testDecision(id)
	d.JobID = job
	if err := (SQLiteSchedulerDecisionWriter{}).Record(ctx, db, d); err != nil {
		t.Fatalf("seed decision: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO `+tableDecisionEconomics+` (decision_id, shadow_price, correlation, utility, predicted_success)
		VALUES (?, 1, 0, ?, 0.5)`, id, u); err != nil {
		t.Fatalf("seed economics: %v", err)
	}
	if kind != "" {
		if _, err := InsertDecisionOutcome(ctx, db, newTestClock(), id, kind); err != nil {
			t.Fatalf("seed outcome: %v", err)
		}
	}
}

func storedRegret(t *testing.T, db *sql.DB, id string) sql.NullFloat64 {
	t.Helper()
	var r sql.NullFloat64
	if err := db.QueryRow(`SELECT regret FROM `+tableDecisionEconomics+` WHERE decision_id = ?`, id).Scan(&r); err != nil {
		t.Fatalf("read regret: %v", err)
	}
	return r
}

func TestRegretFillIdempotent(t *testing.T) {
	db := openMigratedDB(t, tmplOutcome)
	seedRegretDecision(t, db, "d1", "job-1", 0.25, OutcomeKindCompleted)
	f := &RegretFiller{DB: db}
	if err := f.Fill(context.Background(), "job-1", 1.0); err != nil {
		t.Fatalf("first Fill: %v", err)
	}
	if got := storedRegret(t, db, "d1"); !got.Valid || got.Float64 != 0.75 {
		t.Fatalf("regret = %+v, want 0.75", got)
	}
	if err := f.Fill(context.Background(), "job-1", 9.0); err != nil {
		t.Fatalf("second Fill: %v", err)
	}
	if got := storedRegret(t, db, "d1"); got.Float64 != 0.75 {
		t.Fatalf("regret after second Fill = %v, want 0.75 unchanged", got.Float64)
	}
}

func TestRegretWrittenOnceOnOutcome(t *testing.T) {
	db := openMigratedDB(t, tmplOutcome)
	seedRegretDecision(t, db, "d1", "job-1", 0.5, OutcomeKindCompleted)
	seedRegretDecision(t, db, "d2", "job-2", 0.5, OutcomeKindFailed)
	seedRegretDecision(t, db, "d3", "job-3", 0.5, OutcomeKindCancelled)
	seedRegretDecision(t, db, "d4", "job-4", 0.5, "")
	if got := storedRegret(t, db, "d1"); got.Valid {
		t.Fatalf("regret before Fill = %+v, want NULL", got)
	}
	ctx := context.Background()
	f := &RegretFiller{DB: db}
	for _, j := range []string{"job-1", "job-2", "job-3", "job-4"} {
		if err := f.Fill(ctx, j, 1.0); err != nil {
			t.Fatalf("Fill %s: %v", j, err)
		}
	}
	s, err := RegretAggregate(ctx, db)
	if err != nil {
		t.Fatalf("RegretAggregate: %v", err)
	}
	if s.Count != 2 || s.Total != 1.0 || s.Avg != 0.5 {
		t.Fatalf("aggregate = %+v, want count 2 total 1 avg 0.5 (completed and failed only)", s)
	}
}

func TestRegretFillUnknownJob(t *testing.T) {
	db := openMigratedDB(t, tmplOutcome)
	seedRegretDecision(t, db, "d1", "job-1", 0.5, OutcomeKindCompleted)
	err := (&RegretFiller{DB: db}).Fill(context.Background(), "nope", 1.0)
	requireSentinel(t, err, ErrDecisionNotFound, "not-found: learn: regret names no recorded scheduler decision economics")
	if got := storedRegret(t, db, "d1"); got.Valid {
		t.Fatalf("unknown-job Fill wrote regret %+v", got)
	}
}

func TestRegretNegativeStored(t *testing.T) {
	db := openMigratedDB(t, tmplOutcome)
	seedRegretDecision(t, db, "d1", "job-1", 0.9, OutcomeKindCompleted)
	if err := (&RegretFiller{DB: db}).Fill(context.Background(), "job-1", 0.4); err != nil {
		t.Fatalf("Fill: %v", err)
	}
	if got := storedRegret(t, db, "d1"); !got.Valid || got.Float64 >= 0 || got.Float64 != 0.4-0.9 {
		t.Fatalf("regret = %+v, want -0.5 unclamped", got)
	}
}
