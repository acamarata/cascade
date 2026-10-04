// Purpose: TestTelemetryOutcomeRoundTrip -- Record, then read back EVERY
//
//	column of jobs_telemetry_outcomes (every allowlisted field plus id and
//	created_at) against the input, for each terminal state, with the usage
//	join and the jobs_usage.outcome_class propagation asserted from stored
//	state (an earlier version read five fields).
//
// SPORT: learn/roundtrip/ADD (P1-CAP-02).
package learn

import (
	"context"
	"database/sql"
	"reflect"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/conductor"
)

// fullOutcome returns an outcome with every allowlisted field non-zero and
// distinct, so a column the writer drops or swaps cannot hide behind a zero.
func fullOutcome(jobID string, final OutcomeClass) TelemetryOutcome {
	rb := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	return TelemetryOutcome{
		JobID: jobID, TaskClass: "code-gen", RepoID: "repo-9f3a", Language: LanguageRust,
		Component: "conductor-core", RiskClass: "elevated", LaneTier: "tier-2", NodeID: "node-7c1d",
		ScopeRef: "scope-E-08", ContextSizeTokens: 4096, RetrievalStrategy: "hybrid-v2",
		DurationMS: 12345, QueueTimeMS: 678, RetryCount: 2, CIFailureCount: 3, ReworkCycles: 4,
		FinalOutcome: final, RollbackAt: &rb, RegressionDetected: true,
		CostTokens: 150, QuotaUnits: 777, // the joined values the usage row below yields
	}
}

// readOutcomeRow reads every column of the row for jobID.
func readOutcomeRow(t *testing.T, db *sql.DB, jobID string) (got TelemetryOutcome, id, createdAt int64) {
	t.Helper()
	var lang, final string
	var rollback sql.NullInt64
	var regression int
	err := db.QueryRow(`SELECT id, job_id, task_class, repo_id, language, component, risk_class, lane_tier,
		node_id, scope_ref, context_size_tokens, retrieval_strategy, duration_ms, queue_time_ms, retry_count,
		ci_failure_count, rework_cycles, final_outcome, rollback_at, regression_detected, cost_tokens,
		quota_units, created_at FROM `+tableTelemetryOutcomes+` WHERE job_id = ?`, jobID).Scan(
		&id, &got.JobID, &got.TaskClass, &got.RepoID, &lang, &got.Component, &got.RiskClass, &got.LaneTier,
		&got.NodeID, &got.ScopeRef, &got.ContextSizeTokens, &got.RetrievalStrategy, &got.DurationMS,
		&got.QueueTimeMS, &got.RetryCount, &got.CIFailureCount, &got.ReworkCycles, &final, &rollback,
		&regression, &got.CostTokens, &got.QuotaUnits, &createdAt)
	if err != nil {
		t.Fatalf("read back %s: %v", jobID, err)
	}
	got.Language, got.FinalOutcome, got.RegressionDetected = Language(lang), OutcomeClass(final), regression == 1
	if rollback.Valid {
		rb := time.Unix(rollback.Int64, 0).UTC()
		got.RollbackAt = &rb
	}
	return got, id, createdAt
}

// assertEveryAllowlistedField compares want and got on every allowlisted
// field and fails if want leaves one at its zero value (an empty
// comparison would pass by construction).
func assertEveryAllowlistedField(t *testing.T, want, got TelemetryOutcome) {
	t.Helper()
	wv, gv := reflect.ValueOf(want), reflect.ValueOf(got)
	for _, name := range sortedOutcomeFields() {
		w, g := wv.FieldByName(name), gv.FieldByName(name)
		if !w.IsValid() || w.IsZero() {
			t.Errorf("fixture bug: field %s is zero in the expected outcome", name)
			continue
		}
		if name == "RollbackAt" {
			if g.IsNil() || !want.RollbackAt.Equal(*got.RollbackAt) {
				t.Errorf("field RollbackAt: got %v, want %v", got.RollbackAt, want.RollbackAt)
			}
			continue
		}
		if !reflect.DeepEqual(w.Interface(), g.Interface()) {
			t.Errorf("field %s: got %v, want %v", name, g.Interface(), w.Interface())
		}
	}
}

// TestTelemetryOutcomeRoundTrip: Record inserts a row whose read-back equals
// the input on every allowlisted field; cost_tokens and quota_units come from
// the jobs_usage row by job_id; jobs_usage.outcome_class is set to the
// terminal state; id and created_at are stamped by the writer.
func TestTelemetryOutcomeRoundTrip(t *testing.T) {
	db := newTestOutcomeDB(t)
	ctx := context.Background()
	store := conductor.NewUsageStore(db)
	w := NewSQLiteOutcomeWriter(db, newTestClock())
	// readOutcomeRow names every column; the table must have no other.
	var cols int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?)`, tableTelemetryOutcomes).Scan(&cols); err != nil {
		t.Fatalf("count columns: %v", err)
	}
	if want := len(sortedOutcomeFields()) + 2; cols != want { // + id, created_at
		t.Fatalf("table has %d columns, the read-back covers %d", cols, want)
	}
	for _, final := range []OutcomeClass{OutcomeAccepted, OutcomeRejected, OutcomeUnknown} {
		jobID := "job-rt-" + string(final)
		rec := conductor.UsageRecord{JobID: conductor.JobID(jobID), LaneID: "l1", TaskClass: "code",
			TokensIn: 100, TokensOut: 50, CostMicroUSD: 777, OutcomeClass: "unknown"}
		if err := store.WriteUsageRecord(ctx, rec); err != nil {
			t.Fatalf("seed usage for %s: %v", jobID, err)
		}
		want := fullOutcome(jobID, final)
		in := want
		in.CostTokens, in.QuotaUnits = 0, 0 // the writer must join them, not trust the caller
		if err := w.Record(ctx, in); err != nil {
			t.Fatalf("Record %s: %v", jobID, err)
		}
		got, id, createdAt := readOutcomeRow(t, db, jobID)
		assertEveryAllowlistedField(t, want, got)
		if id <= 0 {
			t.Errorf("%s: id = %d, want a stamped autoincrement id", jobID, id)
		}
		if createdAt != newTestClock().Now().Unix() {
			t.Errorf("%s: created_at = %d, want the injected clock's %d", jobID, createdAt, newTestClock().Now().Unix())
		}
		var class string
		if err := db.QueryRow(`SELECT outcome_class FROM jobs_usage WHERE job_id = ?`, jobID).Scan(&class); err != nil {
			t.Fatalf("read outcome_class for %s: %v", jobID, err)
		}
		if class != string(final) {
			t.Errorf("%s: jobs_usage.outcome_class = %q, want %q", jobID, class, final)
		}
	}
}
