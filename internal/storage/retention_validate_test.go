// Purpose: CR-required tests for the two BLOCKING defects: R-14.144
//
//	(cross-domain target refusal) and R-14.145 (non-positive BatchCap
//	refusal). Timestamp-value/column-type edge cases (nit 3, nit 4) live
//	in the sibling retention_timestamp_test.go — split under R-14.117's
//	300-line cap. Shared helpers live in retention_helpers_test.go.
//
// SPORT: internal.storage.retention.DomainPruner/ADDED (P1-E02-W1-S03-T2).

package storage

import (
	"context"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/testkit"
)

// --- R-14.144: cross-domain target refusal -------------------------------

// TestPrune_RejectsCrossDomainTarget proves the exact CR-probed defect is
// fixed: registering DomainSessions against DomainConfig's table no
// longer deletes that table's rows and returns nil — it is refused as a
// configuration error before any DELETE runs, and the foreign table's row
// survives untouched.
func TestPrune_RejectsCrossDomainTarget(t *testing.T) {
	db := openRetentionTestDB(t)
	createTSTable(t, db, "config_domain_root")

	now := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	clock := testkit.NewFrozenClock(now)
	old := now.Add(-48 * time.Hour).Unix()
	insertTSRow(t, db, "config_domain_root", old, "belongs-to-config")

	cfg := RetentionConfig{
		DomainRetention: map[DomainID]time.Duration{DomainSessions: time.Hour},
		Targets: map[DomainID][]PruneTarget{
			// Copy-pasted cross-domain table: registered under
			// DomainSessions but names DomainConfig's table.
			DomainSessions: {{Table: "config_domain_root", TimestampColumn: "ts"}},
		},
		Clock: clock,
	}
	reports, err := (DomainPruner{}).Prune(context.Background(), db, cfg)
	if err == nil {
		t.Fatal("Prune err = nil, want a refusal for the cross-domain target")
	}
	report := findReport(t, reports, DomainSessions)
	if report.Err == nil {
		t.Error("DomainSessions report.Err = nil, want the cross-domain refusal")
	}
	if report.RowsDeleted != 0 {
		t.Errorf("DomainSessions RowsDeleted = %d, want 0 (refused before any DELETE)", report.RowsDeleted)
	}
	if got := countRows(t, db, "config_domain_root"); got != 1 {
		t.Errorf("config_domain_root rows = %d, want 1 (a different domain's row must never be deleted by this config error)", got)
	}
}

// TestPrune_CorrectlyScopedTargetStillWorks proves the R-14.144 fix does
// not collaterally break a legitimately-scoped target: a table prefixed
// with its own domain's TablePrefix still prunes normally.
func TestPrune_CorrectlyScopedTargetStillWorks(t *testing.T) {
	db := openRetentionTestDB(t)
	createTSTable(t, db, "sessions_domain_root")

	now := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	clock := testkit.NewFrozenClock(now)
	old := now.Add(-48 * time.Hour).Unix()
	insertTSRow(t, db, "sessions_domain_root", old, "stale-session")

	cfg := RetentionConfig{
		DomainRetention: map[DomainID]time.Duration{DomainSessions: time.Hour},
		Targets: map[DomainID][]PruneTarget{
			DomainSessions: {{Table: "sessions_domain_root", TimestampColumn: "ts"}},
		},
		Clock: clock,
	}
	reports, err := (DomainPruner{}).Prune(context.Background(), db, cfg)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	report := findReport(t, reports, DomainSessions)
	if report.Err != nil || report.RowsDeleted != 1 {
		t.Errorf("DomainSessions report = %+v, want RowsDeleted=1, Err=nil", report)
	}
	if got := countRows(t, db, "sessions_domain_root"); got != 0 {
		t.Errorf("sessions_domain_root rows = %d, want 0", got)
	}
}

// --- R-14.145: non-positive BatchCap refusal ------------------------------

// TestPrune_RejectsNegativeBatchCap proves the exact CR-probed defect is
// fixed: a negative BatchCap no longer silently makes deleteOlderThan's
// round count negative (skipping the delete loop, 0 rows deleted, nil
// error) — Prune now refuses it as a configuration error up front, and no
// row anywhere is deleted.
func TestPrune_RejectsNegativeBatchCap(t *testing.T) {
	db := openRetentionTestDB(t)
	createTSTable(t, db, "queue_domain_root")

	now := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	clock := testkit.NewFrozenClock(now)
	old := now.Add(-48 * time.Hour).Unix()
	insertTSRow(t, db, "queue_domain_root", old, "should-not-survive-by-accident")

	cfg := RetentionConfig{
		DomainRetention: map[DomainID]time.Duration{DomainQueue: time.Hour},
		Targets: map[DomainID][]PruneTarget{
			DomainQueue: {{Table: "queue_domain_root", TimestampColumn: "ts"}},
		},
		BatchCap: -1,
		Clock:    clock,
	}
	reports, err := (DomainPruner{}).Prune(context.Background(), db, cfg)
	if err == nil {
		t.Fatal("Prune err = nil, want a refusal for the negative BatchCap")
	}
	if len(reports) != 0 {
		t.Errorf("reports = %+v, want none — BatchCap is validated before any per-domain work starts", reports)
	}
	if got := countRows(t, db, "queue_domain_root"); got != 1 {
		t.Errorf("queue_domain_root rows = %d, want 1 (negative BatchCap must not silently retain via a skipped loop AND must not delete either)", got)
	}
}

// TestPrune_ZeroBatchCapNormalizesToDefault proves a zero BatchCap is
// still the documented "use the default" signal, not itself rejected —
// Normalize fills it in with defaultPruneBatchCap before validateBatchCap
// ever sees it, so Prune succeeds and actually deletes.
func TestPrune_ZeroBatchCapNormalizesToDefault(t *testing.T) {
	db := openRetentionTestDB(t)
	createTSTable(t, db, "jobs_domain_root")

	now := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	clock := testkit.NewFrozenClock(now)
	old := now.Add(-48 * time.Hour).Unix()
	insertTSRow(t, db, "jobs_domain_root", old, "stale-job")

	cfg := RetentionConfig{
		DomainRetention: map[DomainID]time.Duration{DomainJobs: time.Hour},
		Targets: map[DomainID][]PruneTarget{
			DomainJobs: {{Table: "jobs_domain_root", TimestampColumn: "ts"}},
		},
		BatchCap: 0,
		Clock:    clock,
	}
	reports, err := (DomainPruner{}).Prune(context.Background(), db, cfg)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	report := findReport(t, reports, DomainJobs)
	if report.Err != nil || report.RowsDeleted != 1 {
		t.Errorf("DomainJobs report = %+v, want RowsDeleted=1, Err=nil", report)
	}
}

// TestPrune_ValidBatchCapWorks proves an explicit positive BatchCap is
// accepted and used as-is.
func TestPrune_ValidBatchCapWorks(t *testing.T) {
	db := openRetentionTestDB(t)
	createTSTable(t, db, "memory_domain_root")

	now := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	clock := testkit.NewFrozenClock(now)
	old := now.Add(-48 * time.Hour).Unix()
	for i := 0; i < 5; i++ {
		insertTSRow(t, db, "memory_domain_root", old, "row")
	}

	cfg := RetentionConfig{
		DomainRetention: map[DomainID]time.Duration{DomainMemory: time.Hour},
		Targets: map[DomainID][]PruneTarget{
			DomainMemory: {{Table: "memory_domain_root", TimestampColumn: "ts"}},
		},
		BatchCap: 2,
		Clock:    clock,
	}
	reports, err := (DomainPruner{}).Prune(context.Background(), db, cfg)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	report := findReport(t, reports, DomainMemory)
	if report.Err != nil || report.RowsDeleted != 5 {
		t.Errorf("DomainMemory report = %+v, want RowsDeleted=5, Err=nil", report)
	}
}
