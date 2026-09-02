// Purpose: nit-3/nit-4 tests — the timestamp-value and column-type edge
//
//	cases the CR found untested (NULL, future, zero, negative values; a
//	wrong-type column) — pinning each behaviour the CR empirically
//	established, plus nit 4's registration-time type-validation decision.
//	Split from retention_validate_test.go under R-14.117's 300-line cap.
//	Shared helpers live in retention_helpers_test.go.
//
// SPORT: internal.storage.retention.DomainPruner/ADDED (P1-E02-W1-S03-T2).

package storage

import (
	"context"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/testkit"
)

// TestPrune_NullTimestampSurvives pins the CR's empirically-established
// behaviour: a NULL timestamp compares false against every cutoff (NULL <
// cutoff is never true in SQL three-valued logic), so the row survives
// every Prune call — safe, but previously unproven by any test.
func TestPrune_NullTimestampSurvives(t *testing.T) {
	db := openRetentionTestDB(t)
	createNullableTSTable(t, db, "context_domain_root")

	now := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	clock := testkit.NewFrozenClock(now)
	insertNullTSRow(t, db, "context_domain_root", "null-ts")

	cfg := RetentionConfig{
		DomainRetention: map[DomainID]time.Duration{DomainContext: time.Hour},
		Targets: map[DomainID][]PruneTarget{
			DomainContext: {{Table: "context_domain_root", TimestampColumn: "ts"}},
		},
		Clock: clock,
	}
	reports, err := (DomainPruner{}).Prune(context.Background(), db, cfg)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	report := findReport(t, reports, DomainContext)
	if report.Err != nil || report.RowsDeleted != 0 {
		t.Errorf("DomainContext report = %+v, want RowsDeleted=0, Err=nil", report)
	}
	if got := countRows(t, db, "context_domain_root"); got != 1 {
		t.Errorf("context_domain_root rows = %d, want 1 (NULL timestamp must survive)", got)
	}
}

// TestPrune_FutureTimestampSurvives pins that a timestamp after the
// cutoff (younger than the window) survives, as documented.
func TestPrune_FutureTimestampSurvives(t *testing.T) {
	db := openRetentionTestDB(t)
	createTSTable(t, db, "audit_domain_root")

	now := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	clock := testkit.NewFrozenClock(now)
	future := now.Add(1000 * time.Hour).Unix()
	insertTSRow(t, db, "audit_domain_root", future, "future-ts")

	cfg := RetentionConfig{
		DomainRetention: map[DomainID]time.Duration{DomainAudit: time.Hour},
		Targets: map[DomainID][]PruneTarget{
			DomainAudit: {{Table: "audit_domain_root", TimestampColumn: "ts"}},
		},
		Clock: clock,
	}
	reports, err := (DomainPruner{}).Prune(context.Background(), db, cfg)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	report := findReport(t, reports, DomainAudit)
	if report.Err != nil || report.RowsDeleted != 0 {
		t.Errorf("DomainAudit report = %+v, want RowsDeleted=0, Err=nil", report)
	}
	if got := countRows(t, db, "audit_domain_root"); got != 1 {
		t.Errorf("audit_domain_root rows = %d, want 1 (future timestamp must survive)", got)
	}
}

// TestPrune_ZeroAndNegativeTimestampsAreDeleted pins the CR's dangerous-
// but-documented finding: zero and negative timestamps are treated as
// maximally old, not as "unset", and ARE deleted on the very first Prune
// call. This is not a bug this ticket fixes — PruneTarget's doc comment
// now warns callers explicitly — but it must be proven, not assumed.
func TestPrune_ZeroAndNegativeTimestampsAreDeleted(t *testing.T) {
	db := openRetentionTestDB(t)
	createTSTable(t, db, "secrets_domain_root")

	now := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	clock := testkit.NewFrozenClock(now)
	insertTSRow(t, db, "secrets_domain_root", 0, "zero-ts")
	insertTSRow(t, db, "secrets_domain_root", -1, "negative-ts")

	cfg := RetentionConfig{
		DomainRetention: map[DomainID]time.Duration{DomainSecrets: time.Hour},
		Targets: map[DomainID][]PruneTarget{
			DomainSecrets: {{Table: "secrets_domain_root", TimestampColumn: "ts"}},
		},
		Clock: clock,
	}
	reports, err := (DomainPruner{}).Prune(context.Background(), db, cfg)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	report := findReport(t, reports, DomainSecrets)
	if report.Err != nil || report.RowsDeleted != 2 {
		t.Errorf("DomainSecrets report = %+v, want RowsDeleted=2 (zero AND negative are both treated as maximally old)", report)
	}
	if got := countRows(t, db, "secrets_domain_root"); got != 0 {
		t.Errorf("secrets_domain_root rows = %d, want 0", got)
	}
}

// TestPrune_WrongTypeColumnRefused pins nit 4's chosen fix: rather than
// looping forever comparing a TEXT column (e.g. ISO8601 text) against an
// integer cutoff — always false, so the CR's "Prune runs forever deleting
// nothing with a nil error" silent no-op — Prune now validates the
// column's declared type at registration and refuses with a configuration
// error before issuing any DELETE. The ISO8601 row is left untouched.
func TestPrune_WrongTypeColumnRefused(t *testing.T) {
	db := openRetentionTestDB(t)
	createWrongTypeTSTable(t, db, "blobs_domain_root")

	now := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	clock := testkit.NewFrozenClock(now)
	insertTextTSRow(t, db, "blobs_domain_root", "2020-01-01T00:00:00Z", "iso8601-ts")

	cfg := RetentionConfig{
		DomainRetention: map[DomainID]time.Duration{DomainBlobs: time.Hour},
		Targets: map[DomainID][]PruneTarget{
			DomainBlobs: {{Table: "blobs_domain_root", TimestampColumn: "ts"}},
		},
		Clock: clock,
	}
	reports, err := (DomainPruner{}).Prune(context.Background(), db, cfg)
	if err == nil {
		t.Fatal("Prune err = nil, want a refusal for the wrong-type TimestampColumn")
	}
	report := findReport(t, reports, DomainBlobs)
	if report.Err == nil {
		t.Error("DomainBlobs report.Err = nil, want the wrong-type-column refusal")
	}
	if report.RowsDeleted != 0 {
		t.Errorf("DomainBlobs RowsDeleted = %d, want 0 (refused before any DELETE)", report.RowsDeleted)
	}
	if got := countRows(t, db, "blobs_domain_root"); got != 1 {
		t.Errorf("blobs_domain_root rows = %d, want 1 (must survive the refusal, not be silently retained by a forever-false comparison either)", got)
	}
}
