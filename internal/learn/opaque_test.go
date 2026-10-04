// Purpose: the opaque-id rules for ScopeRef, labels and JobID, proven
//
//	against a real SQLite database: a locating ScopeRef or a dotted label
//	stores zero rows, a JobID the column cannot hold is stored as its
//	"job-" digest id by both writers (the usage join keeps the raw id),
//	and a negative finding Count is refused.
//
// SPORT: learn/opaque/ADD (P1-CAP-02).
package learn

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/pkg/cascade"
)

// assertRefusedNoRows records o and requires a value-free KindInvalidInput
// refusal that leaves both telemetry tables empty.
func assertRefusedNoRows(t *testing.T, name, value string, o TelemetryOutcome) {
	t.Helper()
	db := newTestOutcomeDB(t)
	err := NewSQLiteOutcomeWriter(db, newTestClock()).Record(context.Background(), o)
	if err == nil || !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("%s: err = %v, want a KindInvalidInput refusal", name, err)
		return
	}
	if strings.Contains(errChainText(err), value) {
		t.Errorf("%s: an error in the chain carries the rejected value", name)
	}
	if n := telemetryRowCount(t, db); n != 0 {
		t.Errorf("%s: rows after the refusal = %d, want 0", name, n)
	}
}

// TestScopeRefOpaqueRule: an e-mail address, URL, home or drive path, host
// name, IP address or client path in ScopeRef is refused before any SQL; an
// opaque scope id stores and reads back byte-identical.
func TestScopeRefOpaqueRule(t *testing.T) {
	for _, v := range []string{
		"person@example.com", "https://example.com/x", "/Users/name/notes", "~/notes",
		`C:\Users\name`, "10.1.2.3", "fe80::1", "clients/acme/contract.md", "build.example.com",
		"alice%40example%2Ecom",
	} {
		assertRefusedNoRows(t, "ScopeRef "+v, v, withField("ScopeRef", v))
	}
	db := newTestOutcomeDB(t)
	o := baseOutcome("job-opaque-scope")
	o.ScopeRef = "scope-" + digestID("clients/acme/contract.md")
	if err := NewSQLiteOutcomeWriter(db, newTestClock()).Record(context.Background(), o); err != nil {
		t.Fatalf("opaque scope refused: %v", err)
	}
	var got string
	if err := db.QueryRow(`SELECT scope_ref FROM `+tableTelemetryOutcomes+` WHERE job_id = ?`, o.JobID).Scan(&got); err != nil {
		t.Fatalf("read scope_ref: %v", err)
	}
	if got != o.ScopeRef {
		t.Errorf("scope_ref = %q, want %q", got, o.ScopeRef)
	}
}

// TestLabelsRefuseHostAndAddress: a host name or IPv4 address in any label
// column is refused and stores nothing.
func TestLabelsRefuseHostAndAddress(t *testing.T) {
	for _, field := range []string{"TaskClass", "RiskClass", "LaneTier", "RetrievalStrategy", "Component"} {
		for _, v := range []string{"a.example.com", "10.1.2.3"} {
			assertRefusedNoRows(t, field+" "+v, v, withField(field, v))
		}
	}
}

// outcomeIDFor returns the stored outcome id and job_id count for jobID.
func outcomeIDFor(t *testing.T, db *sql.DB, jobID string) (id int64, rows int) {
	t.Helper()
	err := db.QueryRow(`SELECT COALESCE(MAX(id), 0), COUNT(*) FROM `+tableTelemetryOutcomes+` WHERE job_id = ?`, jobID).Scan(&id, &rows)
	if err != nil {
		t.Fatalf("read outcome for job id: %v", err)
	}
	return id, rows
}

// TestUnstorableJobIDStoredAsDigest: a free-text planner id and a 65-char id
// are stored as "job-"+digest, never raw; the usage join and the
// outcome_class update use the raw id; WriteFinding given the same raw id
// attaches to that outcome.
func TestUnstorableJobIDStoredAsDigest(t *testing.T) {
	for _, raw := range []string{"intent:fix the login bug", strings.Repeat("j", 65)} {
		db := newTestOutcomeDB(t)
		ctx := context.Background()
		rec := conductor.UsageRecord{JobID: conductor.JobID(raw), LaneID: "l1", TaskClass: "code",
			TokensIn: 10, TokensOut: 5, CostMicroUSD: 42, OutcomeClass: "unknown"}
		if err := conductor.NewUsageStore(db).WriteUsageRecord(ctx, rec); err != nil {
			t.Fatalf("seed usage: %v", err)
		}
		if err := NewSQLiteOutcomeWriter(db, newTestClock()).Record(ctx, baseOutcome(raw)); err != nil {
			t.Fatalf("Record with an unstorable job id: %v", err)
		}
		if _, n := outcomeIDFor(t, db, raw); n != 0 {
			t.Errorf("raw job id stored in %d rows, want 0", n)
		}
		id, n := outcomeIDFor(t, db, "job-"+digestID(raw))
		if n != 1 {
			t.Fatalf("digest job id rows = %d, want 1", n)
		}
		var cost, quota int64
		var class string
		if err := db.QueryRow(`SELECT o.cost_tokens, o.quota_units, u.outcome_class FROM `+tableTelemetryOutcomes+
			` o, jobs_usage u WHERE o.id = ? AND u.job_id = ?`, id, raw).Scan(&cost, &quota, &class); err != nil {
			t.Fatalf("read joined values: %v", err)
		}
		if cost != 15 || quota != 42 || class != string(OutcomeAccepted) {
			t.Errorf("cost=%d quota=%d class=%q, want 15, 42, accepted from the raw-id usage row", cost, quota, class)
		}
		f := Finding{JobID: raw, Family: FamilyReview, Category: CategoryStyle, Severity: SeverityLow, Count: 2}
		if err := NewSQLiteFindingWriter(db).WriteFinding(ctx, f); err != nil {
			t.Fatalf("WriteFinding with the same raw job id: %v", err)
		}
		var attached int
		if err := db.QueryRow(`SELECT COUNT(*) FROM `+tableTelemetryFinding+` WHERE outcome_id = ?`, id).Scan(&attached); err != nil {
			t.Fatalf("count findings: %v", err)
		}
		if attached != 1 {
			t.Errorf("findings attached to the digest outcome = %d, want 1", attached)
		}
	}
}

// TestFindingRefusesNegativeCount: Count -5 is refused before any SQL and
// stores no finding row; Count 0 stores.
func TestFindingRefusesNegativeCount(t *testing.T) {
	db := newTestOutcomeDB(t)
	ctx := context.Background()
	if err := NewSQLiteOutcomeWriter(db, newTestClock()).Record(ctx, baseOutcome("job-count-1")); err != nil {
		t.Fatalf("seed outcome: %v", err)
	}
	fw := NewSQLiteFindingWriter(db)
	f := Finding{JobID: "job-count-1", Family: FamilyReview, Category: CategoryStyle, Severity: SeverityLow, Count: -5}
	if err := fw.WriteFinding(ctx, f); err == nil || !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("Count -5: err = %v, want KindInvalidInput", err)
	}
	if n := countRows(t, db, tableTelemetryFinding); n != 0 {
		t.Fatalf("finding rows after a negative Count = %d, want 0", n)
	}
	f.Count = 0
	if err := fw.WriteFinding(ctx, f); err != nil {
		t.Fatalf("Count 0 refused: %v", err)
	}
	if n := countRows(t, db, tableTelemetryFinding); n != 1 {
		t.Errorf("finding rows after Count 0 = %d, want 1", n)
	}
}
