// Purpose: the identifying-column gate and the value-free error rule,
//
//	proven against a real SQLite database: a rejected input stores zero
//	rows, and no returned error (or wrapped cause) carries the rejected
//	value.
//
// SPORT: learn/validate/ADD (P1-CAP-02).
package learn

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/pkg/cascade"
)

// telemetryRowCount counts rows over both telemetry tables.
func telemetryRowCount(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	err := db.QueryRow(`SELECT (SELECT COUNT(*) FROM ` + tableTelemetryOutcomes +
		`) + (SELECT COUNT(*) FROM ` + tableTelemetryFinding + `)`).Scan(&n)
	if err != nil {
		t.Fatalf("count telemetry rows: %v", err)
	}
	return n
}

// withField returns baseOutcome with one string field replaced.
func withField(field, value string) TelemetryOutcome {
	o := baseOutcome("job-validate-1")
	reflect.ValueOf(&o).Elem().FieldByName(field).SetString(value)
	return o
}

// TestIdentifyingValuesRefusedBeforeStore: an identifying RepoID, an
// identifying NodeID, a 65-character component and every other identifying
// or label column that breaks its rule is refused with KindInvalidInput and
// leaves zero rows in both telemetry tables.
func TestIdentifyingValuesRefusedBeforeStore(t *testing.T) {
	long := func(n int) string { return strings.Repeat("a", n) }
	cases := []struct{ field, value string }{
		{"RepoID", "https://private.example/repository"},
		{"RepoID", "/srv/private/repository"},
		{"NodeID", "person@example.com"},
		{"NodeID", "build-host.example.com"},
		{"Component", long(65)},
		{"TaskClass", strings.Repeat("private task text ", 8)},
		{"RiskClass", strings.Repeat("private risk text ", 8)},
		{"LaneTier", strings.Repeat("private lane text ", 8)},
		{"RetrievalStrategy", strings.Repeat("private retrieval text ", 8)},
		{"ScopeRef", long(65)},
		{"RepoID", long(65)},
	}
	db := newTestOutcomeDB(t) // refusals never write: one db serves every case
	for i, tc := range cases {
		t.Run(fmt.Sprintf("%s_%d", tc.field, i), func(t *testing.T) {
			err := NewSQLiteOutcomeWriter(db, newTestClock()).Record(context.Background(), withField(tc.field, tc.value))
			if err == nil {
				t.Fatalf("%s accepted an identifying or over-long value", tc.field)
			}
			if !cascade.HasKind(err, cascade.KindInvalidInput) {
				t.Errorf("err kind: got %v, want KindInvalidInput", err)
			}
			if n := telemetryRowCount(t, db); n != 0 {
				t.Errorf("rows after a refused %s = %d, want 0", tc.field, n)
			}
		})
	}
}

// TestIdentifyingBoundsAccepted guards the other direction: a 64-character
// component and the neutral defaults store, so the rules are not simply
// refusing everything.
func TestIdentifyingBoundsAccepted(t *testing.T) {
	db := newTestOutcomeDB(t)
	w := NewSQLiteOutcomeWriter(db, newTestClock())
	o := baseOutcome("job-bounds-1")
	o.Component = strings.Repeat("c", 64)
	if err := w.Record(context.Background(), o); err != nil {
		t.Fatalf("a 64-character component refused: %v", err)
	}
	d := outcomeFromJob(baseJobForReconcile("job-bounds-2"), jobs.JobStateAccepted)
	if err := w.Record(context.Background(), d); err != nil {
		t.Fatalf("the reconciler's neutral defaults refused: %v", err)
	}
	if n := telemetryRowCount(t, db); n != 2 {
		t.Errorf("rows = %d, want 2", n)
	}
}

// TestReconcilerCoercesUnstorableScope: every non-empty job scope (short
// or long) becomes an opaque digest id, an empty scope stays empty, and a
// class string that breaks the label rule becomes "unknown" instead of
// wedging the reconcile loop.
func TestReconcilerCoercesUnstorableScope(t *testing.T) {
	for _, scope := range []string{"repo:/tmp/x", "repo:/" + strings.Repeat("deep/", 30)} {
		j := baseJobForReconcile("job-coerce-0")
		j.MutableScope = scope
		got := outcomeFromJob(j, jobs.JobStateAccepted).ScopeRef
		if got != "scope-"+digestID(scope) || strings.Contains(got, "/") {
			t.Errorf("scope of %d chars became %q, want scope- plus its 16-hex digest", len(scope), got)
		}
	}
	empty := baseJobForReconcile("job-coerce-empty")
	empty.MutableScope = ""
	if got := outcomeFromJob(empty, jobs.JobStateAccepted).ScopeRef; got != "" {
		t.Errorf("empty scope became %q, want empty", got)
	}
	j := baseJobForReconcile("job-coerce-1")
	j.MutableScope = "repo:/" + strings.Repeat("deep/", 30)
	j.RiskClass = "has spaces and @ signs"
	o := outcomeFromJob(j, jobs.JobStateAccepted)
	if o.RiskClass != "unknown" {
		t.Errorf("unstorable risk class became %q, want unknown", o.RiskClass)
	}
	db := newTestOutcomeDB(t)
	if err := NewSQLiteOutcomeWriter(db, newTestClock()).Record(context.Background(), o); err != nil {
		t.Fatalf("coerced outcome refused: %v", err)
	}
}

// errChainText joins the message of err and every wrapped cause.
func errChainText(err error) string {
	var parts []string
	for e := err; e != nil; e = errors.Unwrap(e) {
		parts = append(parts, e.Error())
	}
	return strings.Join(parts, " | ")
}

// TestValidationErrorsOmitRawValues: every writer validation error names
// the field and the rule and never contains the rejected value, for a
// credential canary in every string input, an identifying RepoID or NodeID,
// a 65-character label and unknown enum values.
func TestValidationErrorsOmitRawValues(t *testing.T) {
	const marker = "rejected-marker-value"
	long65 := strings.Repeat("L", 65)
	db := newTestOutcomeDB(t)
	w := NewSQLiteOutcomeWriter(db, newTestClock())
	check := func(name, value string, err error) {
		t.Helper()
		if err == nil {
			t.Errorf("%s: no refusal", name)
			return
		}
		if text := errChainText(err); strings.Contains(text, value) {
			t.Errorf("%s: an error in the chain carries the rejected value", name)
		}
	}
	for _, field := range []string{"JobID", "TaskClass", "RepoID", "Language", "Component", "RiskClass",
		"LaneTier", "NodeID", "ScopeRef", "RetrievalStrategy", "FinalOutcome"} {
		check("canary in "+field, credentialCanary, w.Record(context.Background(), withField(field, credentialCanary)))
	}
	for field, value := range map[string]string{
		"RepoID": "https://private.example/repo", "NodeID": "person@example.com", "Component": long65,
		"Language": marker, "FinalOutcome": marker,
	} {
		check("rejected "+field, value, w.Record(context.Background(), withField(field, value)))
	}
	fw := NewSQLiteFindingWriter(db)
	if err := w.Record(context.Background(), baseOutcome("job-validate-1")); err != nil {
		t.Fatalf("seed outcome: %v", err)
	}
	base := Finding{JobID: "job-validate-1", Family: FamilyReview, Category: CategoryStyle, Severity: SeverityLow, Count: 1}
	for _, field := range []string{"JobID", "Family", "Category", "Severity"} {
		for _, value := range []string{credentialCanary, marker} {
			f := base
			reflect.ValueOf(&f).Elem().FieldByName(field).SetString(value)
			check("finding "+field, value, fw.WriteFinding(context.Background(), f))
		}
	}
	missing := base
	missing.JobID = "job-without-an-outcome"
	check("finding for a job with no outcome", missing.JobID, fw.WriteFinding(context.Background(), missing))
	if n := telemetryRowCount(t, db); n != 1 {
		t.Errorf("rows after every refusal = %d, want only the one seeded outcome", n)
	}
}

// TestValidationErrorsNameFieldAndRule: a refusal says which field broke
// which rule, so the value-free message is still actionable.
func TestValidationErrorsNameFieldAndRule(t *testing.T) {
	db := newTestOutcomeDB(t)
	err := NewSQLiteOutcomeWriter(db, newTestClock()).Record(context.Background(), withField("NodeID", "person@example.com"))
	if err == nil {
		t.Fatal("identifying NodeID accepted")
	}
	for _, want := range []string{`"NodeID"`, "opaque id"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err.Error(), want)
		}
	}
}
