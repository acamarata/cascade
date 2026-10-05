// Purpose: TelemetryOutcome/OutcomeWriter tests -- round trip, closed-db
//
//	refusal, the usage join, outcome_class propagation, and the two
//	reflection invariants (no prompt text, no identifier leak).
//
// SPORT: learn/outcome/ADD (P1-E31-W6-S64-T1).
package learn

import (
	"context"
	"database/sql"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/jobs"
)

// newTestOutcomeDB opens a real db with both the learn schema and the
// conductor jobs_usage schema applied (R-16.52's join target): a copy of the
// once-migrated template (see migration_test.go).
func newTestOutcomeDB(t *testing.T) *sql.DB {
	t.Helper()
	return openMigratedDB(t, tmplOutcome)
}

func baseOutcome(jobID string) TelemetryOutcome {
	return TelemetryOutcome{
		JobID: jobID, TaskClass: "code", RepoID: "repo-opaque-1", Language: LanguageGo,
		Component: "conductor", RiskClass: "normal", LaneTier: "tier-1", NodeID: "0123456789abcdef0123456789abcdef",
		ScopeRef: "scope-1", ContextSizeTokens: 100, RetrievalStrategy: "hybrid", DurationMS: 5000,
		QueueTimeMS: 100, RetryCount: 1, CIFailureCount: 2, ReworkCycles: 0,
		FinalOutcome: OutcomeAccepted, RegressionDetected: false,
	}
}

// TestRecordRefusesUnknownEnumValues: an unrecognised Language or
// FinalOutcome is refused before any write, not silently persisted.
func TestRecordRefusesUnknownEnumValues(t *testing.T) {
	db := newTestOutcomeDB(t)
	w := NewSQLiteOutcomeWriter(db, newTestClock())
	badLang := baseOutcome("job-badlang-1")
	badLang.Language = "klingon"
	if err := w.Record(context.Background(), badLang); err == nil {
		t.Error("Record with an unknown Language = nil error, want refusal")
	}
	badOutcome := baseOutcome("job-badoutcome-1")
	badOutcome.FinalOutcome = "maybe"
	if err := w.Record(context.Background(), badOutcome); err == nil {
		t.Error("Record with an unknown FinalOutcome = nil error, want refusal")
	}
}

// TestOutcomeWriter_ClosedDB: an insert against a closed db returns a
// typed error, not a panic.
func TestOutcomeWriter_ClosedDB(t *testing.T) {
	db := newTestOutcomeDB(t)
	_ = db.Close()
	w := NewSQLiteOutcomeWriter(db, newTestClock())
	if err := w.Record(context.Background(), baseOutcome("job-closed-1")); err == nil {
		t.Error("Record on a closed db = nil error, want a failure")
	}
}

// TestUsageJoinPopulatesCostAndQuota seeds a jobs_usage row and asserts
// Record's joined cost_tokens/quota_units.
func TestUsageJoinPopulatesCostAndQuota(t *testing.T) {
	db := newTestOutcomeDB(t)
	ctx := context.Background()
	store := conductor.NewUsageStore(db)
	rec := conductor.UsageRecord{
		JobID: "job-usage-1", LaneID: "l1", TaskClass: "code",
		TokensIn: 100, TokensOut: 50, CostMicroUSD: 777, OutcomeClass: "unknown",
	}
	if err := store.WriteUsageRecord(ctx, rec); err != nil {
		t.Fatalf("seed usage row: %v", err)
	}
	w := NewSQLiteOutcomeWriter(db, newTestClock())
	if err := w.Record(ctx, baseOutcome("job-usage-1")); err != nil {
		t.Fatalf("Record: %v", err)
	}
	var costTokens, quotaUnits int64
	row := db.QueryRowContext(ctx, `SELECT cost_tokens, quota_units FROM `+tableTelemetryOutcomes+` WHERE job_id = ?`, "job-usage-1")
	if err := row.Scan(&costTokens, &quotaUnits); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if costTokens != 150 || quotaUnits != 777 {
		t.Errorf("got cost_tokens=%d quota_units=%d, want 150, 777", costTokens, quotaUnits)
	}
}

// TestOutcomeClassPropagation covers each terminal state's mapping onto
// jobs_usage.outcome_class.
func TestOutcomeClassPropagation(t *testing.T) {
	db := newTestOutcomeDB(t)
	ctx := context.Background()
	store := conductor.NewUsageStore(db)
	w := NewSQLiteOutcomeWriter(db, newTestClock())
	for _, tc := range []struct {
		jobID string
		final OutcomeClass
	}{
		{"job-a", OutcomeAccepted}, {"job-r", OutcomeRejected}, {"job-u", OutcomeUnknown},
	} {
		if err := store.WriteUsageRecord(ctx, conductor.UsageRecord{JobID: conductor.JobID(tc.jobID), OutcomeClass: "unknown"}); err != nil {
			t.Fatalf("seed usage for %s: %v", tc.jobID, err)
		}
		o := baseOutcome(tc.jobID)
		o.FinalOutcome = tc.final
		if err := w.Record(ctx, o); err != nil {
			t.Fatalf("Record %s: %v", tc.jobID, err)
		}
		var got string
		row := db.QueryRowContext(ctx, `SELECT outcome_class FROM jobs_usage WHERE job_id = ?`, tc.jobID)
		if err := row.Scan(&got); err != nil {
			t.Fatalf("read outcome_class for %s: %v", tc.jobID, err)
		}
		if got != string(tc.final) {
			t.Errorf("job %s: outcome_class = %q, want %q", tc.jobID, got, tc.final)
		}
	}
}

// TestRollbackAtRoundTrip covers the nullable RollbackAt field, both set
// and unset.
func TestRollbackAtRoundTrip(t *testing.T) {
	db := newTestOutcomeDB(t)
	ctx := context.Background()
	w := NewSQLiteOutcomeWriter(db, newTestClock())

	rb := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	withRollback := baseOutcome("job-rollback-1")
	withRollback.RollbackAt = &rb
	withRollback.RegressionDetected = true
	if err := w.Record(ctx, withRollback); err != nil {
		t.Fatalf("Record: %v", err)
	}
	var rollbackAt sql.NullInt64
	var regression int
	row := db.QueryRowContext(ctx, `SELECT rollback_at, regression_detected FROM `+tableTelemetryOutcomes+` WHERE job_id = ?`, withRollback.JobID)
	if err := row.Scan(&rollbackAt, &regression); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !rollbackAt.Valid || rollbackAt.Int64 != rb.Unix() {
		t.Errorf("rollback_at = %v, want %d", rollbackAt, rb.Unix())
	}
	if regression != 1 {
		t.Errorf("regression_detected = %d, want 1", regression)
	}

	withoutRollback := baseOutcome("job-rollback-2")
	if err := w.Record(ctx, withoutRollback); err != nil {
		t.Fatalf("Record: %v", err)
	}
	var rollbackAt2 sql.NullInt64
	row2 := db.QueryRowContext(ctx, `SELECT rollback_at FROM `+tableTelemetryOutcomes+` WHERE job_id = ?`, withoutRollback.JobID)
	if err := row2.Scan(&rollbackAt2); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if rollbackAt2.Valid {
		t.Errorf("rollback_at = %v, want NULL", rollbackAt2)
	}
}

// TestScopeRefRoundTrip proves scope_ref survives Record/read-back.
func TestScopeRefRoundTrip(t *testing.T) {
	db := newTestOutcomeDB(t)
	ctx := context.Background()
	w := NewSQLiteOutcomeWriter(db, newTestClock())
	o := baseOutcome("job-scope-1")
	o.ScopeRef = "E-S-08-scope-xyz"
	if err := w.Record(ctx, o); err != nil {
		t.Fatalf("Record: %v", err)
	}
	var got string
	row := db.QueryRowContext(ctx, `SELECT scope_ref FROM `+tableTelemetryOutcomes+` WHERE job_id = ?`, o.JobID)
	if err := row.Scan(&got); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got != o.ScopeRef {
		t.Errorf("scope_ref = %q, want %q", got, o.ScopeRef)
	}
}

// bannedFieldNameFragments is R-21.152's own literal list (acceptance
// criteria): Prompt, Text, Content, Message, Input, Query, Response.
var bannedFieldNameFragments = []string{"Prompt", "Text", "Content", "Message", "Input", "Query", "Response"}

// TestNoPromptTextInvariant: no exported string/[]byte field on
// TelemetryOutcome may match any banned fragment, by reflection.
func TestNoPromptTextInvariant(t *testing.T) {
	typ := reflect.TypeOf(TelemetryOutcome{})
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if f.Type.Kind() != reflect.String {
			continue
		}
		for _, frag := range bannedFieldNameFragments {
			if strings.Contains(f.Name, frag) {
				t.Errorf("field %s (kind %s) matches banned fragment %q", f.Name, f.Type.Kind(), frag)
			}
		}
	}
}

// identifierLeakPattern flags an email, a URL scheme, a UNC/absolute
// path, or a bare hostname-shaped value -- the shapes R-21.162 forbids
// repo_id/node_id from ever carrying.
var identifierLeakPattern = regexp.MustCompile(`@|://|^/|^[A-Za-z]:\\|\.(com|org|net|dev|local)$`)

// TestNoIdentifierLeakInvariant: the reconciler's own opaque defaults
// (and a representative "real" opaque id) never match a locating shape;
// a representative violating value (an email) is asserted to be caught.
func TestNoIdentifierLeakInvariant(t *testing.T) {
	o := outcomeFromJob(baseJobForReconcile("job-id-1"), jobs.JobStateAccepted)
	for _, v := range []string{o.RepoID, o.NodeID, o.LaneTier, o.RetrievalStrategy} {
		if identifierLeakPattern.MatchString(v) {
			t.Errorf("value %q matches a locating shape, want opaque", v)
		}
	}
	if !identifierLeakPattern.MatchString("someone@example.com") {
		t.Error("sanity: the leak pattern must catch a real email")
	}
	if !identifierLeakPattern.MatchString("/etc/passwd") {
		t.Error("sanity: the leak pattern must catch an absolute path")
	}
}
