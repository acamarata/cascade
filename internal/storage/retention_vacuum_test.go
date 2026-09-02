// Purpose: RetentionConfig.Normalize's golden weekly-default test plus
//
//	VacuumJob.Run's full contract (shrink, WAL-checkpoint proof,
//	in-memory refusal, closed-db error paths) — split from
//	retention_test.go per R-14.117 (Art.10.3's 300-line cap; mechanical
//	relocation, no behavior change). Shared helpers live in
//	retention_helpers_test.go.
//
// SPORT: internal.storage.retention.VacuumJob/ADDED,
//
//	internal.storage.retention.RetentionConfig/ADDED (P1-E02-W1-S03-T2).

package storage

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver

	"github.com/acamarata/cascade/internal/testkit"
)

// --- RetentionConfig.Normalize golden test ------------------------------

// TestRetentionConfig_Normalize_WeeklyDefault golden-asserts the plan's
// "weekly VACUUM job" cadence: a zero VacuumInterval normalizes to
// exactly 168h, and a zero BatchCap normalizes to the documented 500-row
// default. An explicit non-zero value on either field is left untouched.
func TestRetentionConfig_Normalize_WeeklyDefault(t *testing.T) {
	got := RetentionConfig{}.Normalize()
	if got.VacuumInterval != 168*time.Hour {
		t.Errorf("VacuumInterval = %v, want 168h", got.VacuumInterval)
	}
	if got.BatchCap != defaultPruneBatchCap {
		t.Errorf("BatchCap = %d, want %d", got.BatchCap, defaultPruneBatchCap)
	}

	explicit := RetentionConfig{VacuumInterval: 42 * time.Hour, BatchCap: 7}.Normalize()
	if explicit.VacuumInterval != 42*time.Hour {
		t.Errorf("explicit VacuumInterval = %v, want unchanged 42h", explicit.VacuumInterval)
	}
	if explicit.BatchCap != 7 {
		t.Errorf("explicit BatchCap = %d, want unchanged 7", explicit.BatchCap)
	}
}

// --- VacuumJob.Run --------------------------------------------------------

// setUpVacuumFixture creates a real modernc-sqlite db, inserts rowCount
// padded rows, fully clears them via Prune (verifying the clear itself),
// and returns the db, the FrozenClock driving it, and the db's on-disk
// main-file path — split out of TestVacuumJob to keep the test itself
// under Art.10.3's 50-line function cap (R-14.117: mechanical split, no
// behavior change).
func setUpVacuumFixture(t *testing.T, rowCount int) (*sql.DB, *testkit.FrozenClock, string) {
	t.Helper()
	db := openRetentionTestDB(t)
	createTSTable(t, db, "blobs_domain_root")

	now := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	clock := testkit.NewFrozenClock(now)
	old := now.Add(-24 * time.Hour).Unix()
	filler := strings.Repeat("x", 200)
	for i := 0; i < rowCount; i++ {
		insertTSRow(t, db, "blobs_domain_root", old, filler)
	}

	pruneCfg := RetentionConfig{
		DomainRetention: map[DomainID]time.Duration{DomainBlobs: time.Hour},
		Targets: map[DomainID][]PruneTarget{
			DomainBlobs: {{Table: "blobs_domain_root", TimestampColumn: "ts"}},
		},
		Clock: clock,
	}
	pruneReports, err := (DomainPruner{}).Prune(context.Background(), db, pruneCfg)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if r := findReport(t, pruneReports, DomainBlobs); r.RowsDeleted != rowCount {
		t.Fatalf("Prune RowsDeleted = %d, want %d (full-window clear)", r.RowsDeleted, rowCount)
	}
	if got := countRows(t, db, "blobs_domain_root"); got != 0 {
		t.Fatalf("rows remaining before vacuum = %d, want 0", got)
	}

	path, err := mainDatabasePath(context.Background(), db)
	if err != nil {
		t.Fatalf("mainDatabasePath: %v", err)
	}
	return db, clock, path
}

// TestVacuumJob inserts 10 000 padded rows, deletes them all via a
// full-clearing Prune (setUpVacuumFixture), runs VacuumJob, and asserts
// (a) the on-disk file shrinks and (b) a follow-up checkpoint finds
// nothing outstanding — the concrete, queryable proof that Run's own
// `PRAGMA wal_checkpoint(FULL)` calls actually executed.
func TestVacuumJob(t *testing.T) {
	const rowCount = 10000
	db, clock, path := setUpVacuumFixture(t, rowCount)

	sizeBeforeVacuum, err := fileSize(path)
	if err != nil {
		t.Fatalf("fileSize before: %v", err)
	}

	report, err := (VacuumJob{Clock: clock}).Run(context.Background(), db)
	if err != nil {
		t.Fatalf("VacuumJob.Run: %v", err)
	}
	if report.FileSizeAfter >= report.FileSizeBefore {
		t.Errorf("FileSizeAfter (%d) >= FileSizeBefore (%d), want a real shrink after deleting %d rows",
			report.FileSizeAfter, report.FileSizeBefore, rowCount)
	}
	if report.FileSizeBefore != sizeBeforeVacuum {
		t.Errorf("report.FileSizeBefore = %d, want the size actually observed before Run (%d)", report.FileSizeBefore, sizeBeforeVacuum)
	}

	sizeAfterVacuum, err := fileSize(path)
	if err != nil {
		t.Fatalf("fileSize after: %v", err)
	}
	if sizeAfterVacuum != report.FileSizeAfter {
		t.Errorf("on-disk size after Run = %d, want report.FileSizeAfter = %d", sizeAfterVacuum, report.FileSizeAfter)
	}

	// PRAGMA wal_checkpoint's three columns are (busy, log, checkpointed):
	// log is how many frames the WAL held AT THE TIME OF THIS CALL, and
	// checkpointed is how many of those this call actually moved into the
	// main file — checkpointed == log (with busy == 0) means a FULLY
	// successful checkpoint, whatever log's absolute value is (a fresh
	// connection issuing its first statement can itself add a frame or
	// two of housekeeping, so log == 0 is not the invariant to assert).
	// This is the concrete, queryable proof that Run's own
	// wal_checkpoint(FULL) calls leave nothing outstanding.
	var busy, log, checkpointed int
	if err := db.QueryRow(`PRAGMA wal_checkpoint(PASSIVE)`).Scan(&busy, &log, &checkpointed); err != nil {
		t.Fatalf("post-Run wal_checkpoint(PASSIVE): %v", err)
	}
	if busy != 0 || checkpointed != log {
		t.Errorf("post-Run wal_checkpoint(PASSIVE) busy=%d log=%d checkpointed=%d, want busy=0 and checkpointed==log (a fully caught-up WAL)",
			busy, log, checkpointed)
	}
}

// TestVacuumJob_InMemoryRefused proves Run refuses an in-memory database
// (no on-disk file to stat or vacuum-shrink) with a clear error rather
// than silently reporting a fabricated zero-byte report.
func TestVacuumJob_InMemoryRefused(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	clock := testkit.NewFrozenClock(time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC))
	if _, err := (VacuumJob{Clock: clock}).Run(context.Background(), db); err == nil {
		t.Error("VacuumJob.Run against :memory: db: err = nil, want a refusal")
	}
}

// TestFileSize_MissingPath proves fileSize surfaces the real os.Stat
// error for a path that does not exist, rather than a fabricated zero.
func TestFileSize_MissingPath(t *testing.T) {
	if _, err := fileSize(filepath.Join(t.TempDir(), "does-not-exist.db")); err == nil {
		t.Error("fileSize(missing path): err = nil, want a stat error")
	}
}

// TestMainDatabasePath_ClosedDB proves mainDatabasePath propagates the
// real driver error when the underlying *sql.DB is already closed, rather
// than reporting a fabricated path.
func TestMainDatabasePath_ClosedDB(t *testing.T) {
	db := openRetentionTestDB(t)
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := mainDatabasePath(context.Background(), db); err == nil {
		t.Error("mainDatabasePath(closed db): err = nil, want the propagated driver error")
	}
}

// TestVacuumJob_Run_ClosedDB proves Run's own resolve-path step surfaces
// the same closed-connection failure to a caller, wrapped in the taxonomy
// (KindUnavailable), rather than panicking or hanging.
func TestVacuumJob_Run_ClosedDB(t *testing.T) {
	db := openRetentionTestDB(t)
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	clock := testkit.NewFrozenClock(time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC))
	if _, err := (VacuumJob{Clock: clock}).Run(context.Background(), db); err == nil {
		t.Error("VacuumJob.Run against a closed db: err = nil, want the propagated failure")
	}
}
