// Purpose: dependent-table retention against real SQLite with
//
//	foreign_keys=ON: children are deleted with their expired parent in one
//	transaction, a child-delete error rolls everything back, and a
//	registration that can never sweep correctly is refused.
//
// SPORT: learn/retention-children/ADD (P1-CAP-02).
package learn

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

const extChildTable = "jobs_ext_child_p1_cap_02"

// newFKDB is newTestOutcomeDB with foreign keys enforced (the single
// connection openTestDB sets makes the PRAGMA stick) and one extension
// child table keyed to jobs_telemetry_outcomes.id.
func newFKDB(t *testing.T) *sql.DB {
	t.Helper()
	db := newTestOutcomeDB(t)
	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		t.Fatalf("enable foreign keys: %v", err)
	}
	var on int
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&on); err != nil || on != 1 {
		t.Fatalf("foreign_keys = %d, err %v; want 1", on, err)
	}
	if _, err := db.Exec(`CREATE TABLE ` + extChildTable +
		` (id INTEGER PRIMARY KEY, outcome_id INTEGER NOT NULL REFERENCES ` + tableTelemetryOutcomes + `(id))`); err != nil {
		t.Fatalf("create %s: %v", extChildTable, err)
	}
	return db
}

// newLearnRegistry is a fresh registry holding learn's own registrations
// plus the extension child, so no test's registration reaches another's.
func newLearnRegistry(t *testing.T) *retentionRegistry {
	t.Helper()
	reg := &retentionRegistry{}
	if err := registerLearnRetention(reg); err != nil {
		t.Fatalf("registerLearnRetention: %v", err)
	}
	if err := reg.addChild(extChildTable, "outcome_id"); err != nil {
		t.Fatalf("add extension child: %v", err)
	}
	return reg
}

// seedWithChildren inserts one outcome at createdAt with one finding and one
// extension row.
func seedWithChildren(t *testing.T, db *sql.DB, jobID string, createdAt int64) {
	t.Helper()
	insertOutcomeAt(t, db, jobID, createdAt)
	var id int64
	if err := db.QueryRow(`SELECT id FROM `+tableTelemetryOutcomes+` WHERE job_id = ?`, jobID).Scan(&id); err != nil {
		t.Fatalf("outcome id: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO `+tableTelemetryFinding+` (outcome_id, family, category, severity, count)
		VALUES (?, 'review', 'style', 'low', 1)`, id); err != nil {
		t.Fatalf("seed finding: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO `+extChildTable+` (outcome_id) VALUES (?)`, id); err != nil {
		t.Fatalf("seed extension row: %v", err)
	}
}

func countRows(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// assertCounts checks outcome, finding and extension row counts together.
func assertCounts(t *testing.T, db *sql.DB, outcomes, findings, ext int) {
	t.Helper()
	for table, want := range map[string]int{tableTelemetryOutcomes: outcomes, tableTelemetryFinding: findings, extChildTable: ext} {
		if got := countRows(t, db, table); got != want {
			t.Errorf("%s rows = %d, want %d", table, got, want)
		}
	}
}

func sweepWith(t *testing.T, db *sql.DB, reg *retentionRegistry, now time.Time) (int64, error) {
	t.Helper()
	s := RetentionSweep{DB: db, Clock: runtime.NewFixedClock(now), Paths: newFixedPaths(t, ""),
		Getenv: noEnv, Environ: noEnviron, registry: reg}
	return s.Run(context.Background())
}

// TestRetentionSweepDeletesChildrenWithParent: finding rows and a registered
// extension child are deleted in the same transaction as their expired
// parent; a younger parent keeps its children; no orphan remains; a child
// delete error leaves every row in place; the FK child no longer blocks.
func TestRetentionSweepDeletesChildrenWithParent(t *testing.T) {
	db := newFKDB(t)
	now := time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)
	old, fresh := now.AddDate(0, 0, -100).Unix(), now.AddDate(0, 0, -10).Unix()
	seedWithChildren(t, db, "job-old-1", old)
	seedWithChildren(t, db, "job-old-2", old)
	seedWithChildren(t, db, "job-fresh-1", fresh)
	assertCounts(t, db, 3, 3, 3)

	// Precondition: the FK really blocks a bare parent delete (the review's
	// SQLITE_CONSTRAINT_FOREIGNKEY), so passing below is meaningful.
	if _, err := db.Exec(`DELETE FROM ` + tableTelemetryOutcomes + ` WHERE job_id = 'job-old-1'`); err == nil ||
		!strings.Contains(err.Error(), "FOREIGN KEY") {
		t.Fatalf("bare parent delete err = %v, want a FOREIGN KEY failure", err)
	}

	// An injected child-delete error rolls every delete back.
	reg := newLearnRegistry(t)
	if _, err := db.Exec(`CREATE TRIGGER refuse_ext_delete BEFORE DELETE ON ` + extChildTable +
		` BEGIN SELECT RAISE(ABORT, 'extension child delete refused'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	n, err := sweepWith(t, db, reg, now)
	if err == nil || !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("sweep with a failing child delete: n=%d err=%v, want KindUnavailable", n, err)
	}
	assertCounts(t, db, 3, 3, 3)

	// Without the injected failure the same data sweeps cleanly.
	if _, err := db.Exec(`DROP TRIGGER refuse_ext_delete`); err != nil {
		t.Fatalf("drop trigger: %v", err)
	}
	n, err = sweepWith(t, db, reg, now)
	if err != nil {
		t.Fatalf("sweep with FK children registered: %v", err)
	}
	if n != 6 { // 2 outcomes + 2 findings + 2 extension rows
		t.Errorf("rows deleted = %d, want 6", n)
	}
	assertCounts(t, db, 1, 1, 1)
	assertOutcomeExists(t, db, "job-old-1", false)
	assertOutcomeExists(t, db, "job-old-2", false)
	assertOutcomeExists(t, db, "job-fresh-1", true)
	for _, child := range []string{tableTelemetryFinding, extChildTable} {
		var orphans int
		if err := db.QueryRow(`SELECT COUNT(*) FROM ` + child + ` c LEFT JOIN ` + tableTelemetryOutcomes +
			` o ON o.id = c.outcome_id WHERE o.id IS NULL`).Scan(&orphans); err != nil {
			t.Fatalf("orphan scan %s: %v", child, err)
		}
		if orphans != 0 {
			t.Errorf("%s has %d orphan rows", child, orphans)
		}
	}
}

// TestRetentionSweepRefusesUnshapedChild: a registered table that exists
// but lacks its named integer column refuses the whole sweep with
// KindInvalidInput before any delete.
func TestRetentionSweepRefusesUnshapedChild(t *testing.T) {
	now := time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)
	for name, register := range map[string]func(*retentionRegistry) error{
		"child without the parent column": func(r *retentionRegistry) error { return r.addChild(extChildTable, "no_such_column") },
		"child column of the wrong type":  func(r *retentionRegistry) error { return r.addChild(extChildTable, "label") },
		"table without the time column":   func(r *retentionRegistry) error { return r.addTable(extChildTable, "created_at") },
	} {
		t.Run(name, func(t *testing.T) {
			db := newFKDB(t)
			if _, err := db.Exec(`ALTER TABLE ` + extChildTable + ` ADD COLUMN label TEXT`); err != nil {
				t.Fatalf("add label column: %v", err)
			}
			seedWithChildren(t, db, "job-old-1", now.AddDate(0, 0, -100).Unix())
			reg := &retentionRegistry{}
			if err := registerLearnRetention(reg); err != nil {
				t.Fatalf("registerLearnRetention: %v", err)
			}
			if err := register(reg); err != nil {
				t.Fatalf("registration refused at registration time: %v", err)
			}
			_, err := sweepWith(t, db, reg, now)
			if err == nil || !cascade.HasKind(err, cascade.KindInvalidInput) {
				t.Fatalf("sweep err = %v, want KindInvalidInput", err)
			}
			assertCounts(t, db, 1, 1, 1)
		})
	}
}

// TestRetentionRegistrationRefusesUnknownShape: RegisterRetentionTable on
// jobs_telemetry_finding refuses for every column (it has no time column),
// and RegisterRetentionChild refuses a table without the named column, a
// duplicate, a non-identifier and the parent itself. Each refusal is
// KindInvalidInput and leaves the registry unchanged.
func TestRetentionRegistrationRefusesUnknownShape(t *testing.T) {
	tablesBefore, childrenBefore := defaultRetention.snapshot()
	var refusals []error
	for _, col := range []string{"created_at", "outcome_id", "id", "count", "family", "severity"} {
		refusals = append(refusals, RegisterRetentionTable(tableTelemetryFinding, col))
	}
	refusals = append(refusals,
		RegisterRetentionChild(tableTelemetryFinding, "created_at"),
		RegisterRetentionChild(tableTelemetryFinding, "no_such_column"),
		RegisterRetentionChild(tableTelemetryFinding, "outcome_id"), // duplicate of init's registration
		RegisterRetentionChild("bad name; DROP TABLE x", "outcome_id"),
		RegisterRetentionChild(tableTelemetryFinding+"_ext", "bad column"),
		RegisterRetentionChild(tableTelemetryOutcomes, "outcome_id"),
		RegisterRetentionTable(tableTelemetryOutcomes, "id"),
		RegisterRetentionTable(tableTelemetryOutcomes, "created_at"), // duplicate of init's registration
	)
	for i, err := range refusals {
		if err == nil {
			t.Errorf("refusal %d: registration accepted", i)
		} else if !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Errorf("refusal %d: err %v, want KindInvalidInput", i, err)
		}
	}
	tablesAfter, childrenAfter := defaultRetention.snapshot()
	if len(tablesAfter) != len(tablesBefore) || len(childrenAfter) != len(childrenBefore) {
		t.Errorf("registry changed by refused registrations: tables %d->%d, children %d->%d",
			len(tablesBefore), len(tablesAfter), len(childrenBefore), len(childrenAfter))
	}
	if len(tablesAfter) == 0 || len(childrenAfter) == 0 {
		t.Errorf("init registered %d tables and %d children, want learn's own of each", len(tablesAfter), len(childrenAfter))
	}
}

// TestRetentionInitRegistersLearnTables: init registered the outcomes table
// by created_at and the finding table as its outcome_id child.
func TestRetentionInitRegistersLearnTables(t *testing.T) {
	tables, children := defaultRetention.snapshot()
	if col, ok := retentionTimeColumn(tables, tableTelemetryOutcomes); !ok || col != "created_at" {
		t.Errorf("outcomes registered = %q, %v; want created_at, true", col, ok)
	}
	found := false
	for _, c := range children {
		found = found || (c.table == tableTelemetryFinding && c.parentIDColumn == "outcome_id")
	}
	if !found {
		t.Error("jobs_telemetry_finding is not registered as an outcome_id child")
	}
}

// TestRetentionSweepExactBoundaryWithChildren: the cutoff second itself
// survives with its children, one second older is deleted with them (the
// review's exact-boundary probe, asserted from stored rows).
func TestRetentionSweepExactBoundaryWithChildren(t *testing.T) {
	db := newFKDB(t)
	now := time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)
	cutoff := now.AddDate(0, 0, -DefaultMaxAgeDays).Unix()
	seedWithChildren(t, db, "job-old", cutoff-1)
	seedWithChildren(t, db, "job-boundary", cutoff)
	seedWithChildren(t, db, "job-fresh", cutoff+1)
	n, err := sweepWith(t, db, newLearnRegistry(t), now)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n != 3 {
		t.Errorf("rows deleted = %d, want 3 (the old outcome, its finding and its extension row)", n)
	}
	assertOutcomeExists(t, db, "job-old", false)
	assertOutcomeExists(t, db, "job-boundary", true)
	assertOutcomeExists(t, db, "job-fresh", true)
	assertCounts(t, db, 2, 2, 2)
}
