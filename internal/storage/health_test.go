// Purpose: StorageHealthCheck tests — green path on a bootstrapped real
//
//	.db (Art.2), then one failure trigger per check: a dropped domain
//	table, a non-WAL journal mode, and a probe-write failure against a
//	read-only database. Every .db file lives in t.TempDir() (Art.7.1);
//	every assertion queries a real modernc-sqlite database directly.
//
// SPORT: internal.storage.health.StorageHealthCheck/ADDED
//
//	(P1-E02-W1-S03-T1).
package storage_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/providers/sqlite"
)

// bootstrappedDB opens a fresh real modernc-sqlite database in t.TempDir()
// and Bootstraps it, returning the *sql.DB and the on-disk path (needed
// by the flock-probe and read-only tests, which reopen the same file).
func bootstrappedDB(t *testing.T) (db *sql.DB, path string) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "cascade.db")
	sdb, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = sdb.Close() })
	clock := testkit.NewFrozenClock(time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC))
	if _, err := storage.Bootstrap(context.Background(), sdb, storage.BootstrapOpts{Clock: clock}); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	return sdb, path
}

// TestStorageHealth_GreenPath proves every check reports OK against a
// freshly bootstrapped real database — no hard-coded pass: this test only
// passes because Bootstrap genuinely created WAL mode, the stamp row,
// every domain table, and the health-probe table the checks actually
// query for.
func TestStorageHealth_GreenPath(t *testing.T) {
	db, _ := bootstrappedDB(t)
	report := storage.StorageHealthCheck(context.Background(), db)

	for name, res := range report.Results() {
		if !res.OK {
			t.Errorf("check %q: OK=false, Detail=%q, Err=%v", name, res.Detail, res.Err)
		}
	}
	if !report.OK() {
		t.Error("HealthReport.OK() = false on a freshly bootstrapped database")
	}
}

// TestStorageHealth_MissingDomainTable drops one domain's anchor table and
// asserts DomainTables flags it via a typed *HealthCheckError, and that
// sqlite_master is what the check actually consulted (not an in-memory
// cache of AllDomains presence).
func TestStorageHealth_MissingDomainTable(t *testing.T) {
	db, _ := bootstrappedDB(t)
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, `DROP TABLE memory_domain_root`); err != nil {
		t.Fatalf("drop memory_domain_root: %v", err)
	}

	report := storage.StorageHealthCheck(ctx, db)
	if report.DomainTables.OK {
		t.Fatal("DomainTables.OK = true after dropping a domain table, want false")
	}
	var hcErr *storage.HealthCheckError
	if !errors.As(report.DomainTables.Err, &hcErr) {
		t.Fatalf("DomainTables.Err = %v (%T), want *storage.HealthCheckError", report.DomainTables.Err, report.DomainTables.Err)
	}
	if hcErr.Check != "domain-tables" {
		t.Errorf("HealthCheckError.Check = %q, want domain-tables", hcErr.Check)
	}
	if !cascade.HasKind(report.DomainTables.Err, cascade.KindIntegrity) {
		t.Errorf("DomainTables.Err kind: want KindIntegrity, got %v", report.DomainTables.Err)
	}

	// Every other check must remain unaffected by the one dropped table.
	if !report.WALMode.OK {
		t.Errorf("WALMode.OK = false, want true (unaffected by a dropped domain table)")
	}
}

// TestStorageHealth_NonWALJournalMode opens a database explicitly switched
// to DELETE journal mode and asserts WALMode flags it.
func TestStorageHealth_NonWALJournalMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cascade.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, `PRAGMA journal_mode=DELETE;`); err != nil {
		t.Fatalf("set journal_mode=DELETE: %v", err)
	}
	clock := testkit.NewFrozenClock(time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC))
	// Bootstrap itself sets WAL — to exercise a genuinely non-WAL
	// database, bootstrap first, then explicitly switch back off WAL.
	if _, err := storage.Bootstrap(ctx, db, storage.BootstrapOpts{Clock: clock}); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if _, err := db.ExecContext(ctx, `PRAGMA journal_mode=DELETE;`); err != nil {
		t.Fatalf("re-set journal_mode=DELETE after Bootstrap: %v", err)
	}

	report := storage.StorageHealthCheck(ctx, db)
	if report.WALMode.OK {
		t.Fatal("WALMode.OK = true with journal_mode=DELETE, want false")
	}
	var hcErr *storage.HealthCheckError
	if !errors.As(report.WALMode.Err, &hcErr) || hcErr.Check != "wal-mode" {
		t.Errorf("WALMode.Err = %v, want *HealthCheckError{Check: wal-mode}", report.WALMode.Err)
	}
}

// TestStorageHealth_ProbeWriteFailsOnReadOnlyDB opens the SAME bootstrapped
// database file a second time through a read-only DSN and asserts
// ProbeWrite is flagged — the round-trip genuinely attempts a write and
// genuinely fails, rather than being told the database is read-only.
func TestStorageHealth_ProbeWriteFailsOnReadOnlyDB(t *testing.T) {
	_, path := bootstrappedDB(t)

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stat bootstrapped db before read-only reopen: %v", err)
	}
	roDB, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatalf("open read-only: %v", err)
	}
	defer func() { _ = roDB.Close() }()

	report := storage.StorageHealthCheck(context.Background(), roDB)
	if report.ProbeWrite.OK {
		t.Fatal("ProbeWrite.OK = true against a read-only database, want false")
	}
	var hcErr *storage.HealthCheckError
	if !errors.As(report.ProbeWrite.Err, &hcErr) || hcErr.Check != "probe-write" {
		t.Errorf("ProbeWrite.Err = %v, want *HealthCheckError{Check: probe-write}", report.ProbeWrite.Err)
	}
}

// TestStorageHealth_SchemaVersionMissingLedger proves the schema-version
// check fails cleanly (not a panic, not a false OK) against a database
// that was never bootstrapped at all.
func TestStorageHealth_SchemaVersionMissingLedger(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cascade.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer func() { _ = db.Close() }()

	report := storage.StorageHealthCheck(context.Background(), db)
	if report.SchemaVersion.OK {
		t.Fatal("SchemaVersion.OK = true against a never-bootstrapped database, want false")
	}
	if report.DomainTables.OK {
		t.Fatal("DomainTables.OK = true against a never-bootstrapped database, want false")
	}
	if report.ProbeWrite.OK {
		t.Fatal("ProbeWrite.OK = true against a never-bootstrapped database, want false")
	}
}

// TestStorageHealth_ReportDoesNotSilentlyPass proves no CheckResult ever
// carries OK:false with a nil Err (Art.1: "no result returns (nil, nil)
// when a check fails").
func TestStorageHealth_ReportDoesNotSilentlyPass(t *testing.T) {
	db, _ := bootstrappedDB(t)
	if _, err := db.ExecContext(context.Background(), `DROP TABLE audit_domain_root`); err != nil {
		t.Fatalf("drop audit_domain_root: %v", err)
	}
	report := storage.StorageHealthCheck(context.Background(), db)
	for name, res := range report.Results() {
		if !res.OK && res.Err == nil {
			t.Errorf("check %q: OK=false but Err=nil (Art.1 forbids a silent failure)", name)
		}
	}
}

// TestHealthCheckError_ErrorString proves HealthCheckError.Error() names
// both the failing check and the wrapped taxonomy error, and that
// errors.As still recovers the underlying *cascade.Error through it
// (Unwrap correctness) so a caller can get the Kind either way.
func TestHealthCheckError_ErrorString(t *testing.T) {
	db, _ := bootstrappedDB(t)
	if _, err := db.ExecContext(context.Background(), `DROP TABLE queue_domain_root`); err != nil {
		t.Fatalf("drop queue_domain_root: %v", err)
	}
	report := storage.StorageHealthCheck(context.Background(), db)

	if report.DomainTables.Err == nil {
		t.Fatal("DomainTables.Err is nil after dropping a domain table")
	}
	msg := report.DomainTables.Err.Error()
	if !strings.Contains(msg, "domain-tables") {
		t.Errorf("HealthCheckError.Error() = %q, want it to name the failing check (domain-tables)", msg)
	}

	var cerr *cascade.Error
	if !errors.As(report.DomainTables.Err, &cerr) {
		t.Fatalf("errors.As(DomainTables.Err, *cascade.Error) failed — Unwrap must expose the taxonomy error")
	}
	if cerr.Kind != cascade.KindIntegrity {
		t.Errorf("recovered cascade.Error.Kind = %v, want KindIntegrity", cerr.Kind)
	}
}

// TestStorageHealth_SchemaVersionBelowFloor directly rewrites the stamped
// schema_version to a value below minimumReaderVersion (0) and asserts
// the check fails with the "below floor" branch, distinct from the
// "ledger table missing entirely" branch TestStorageHealth_
// SchemaVersionMissingLedger already covers.
func TestStorageHealth_SchemaVersionBelowFloor(t *testing.T) {
	db, _ := bootstrappedDB(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `UPDATE applied_migrations SET schema_version = 0`); err != nil {
		t.Fatalf("rewrite schema_version: %v", err)
	}

	report := storage.StorageHealthCheck(ctx, db)
	if report.SchemaVersion.OK {
		t.Fatal("SchemaVersion.OK = true with schema_version rewritten below the floor, want false")
	}
	var hcErr *storage.HealthCheckError
	if !errors.As(report.SchemaVersion.Err, &hcErr) || hcErr.Check != "schema-version" {
		t.Errorf("SchemaVersion.Err = %v, want *HealthCheckError{Check: schema-version}", report.SchemaVersion.Err)
	}
}

// TestStorageHealth_FlockProbe_InMemorySkipped proves the flock-probe
// check reports OK and skips gracefully against an in-memory database,
// where mainDBFilePath resolves to an empty path — there is nothing on
// disk to flock.
func TestStorageHealth_FlockProbe_InMemorySkipped(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open :memory:: %v", err)
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	clock := testkit.NewFrozenClock(time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC))
	if _, err := storage.Bootstrap(ctx, db, storage.BootstrapOpts{Clock: clock}); err != nil {
		t.Fatalf("Bootstrap :memory:: %v", err)
	}

	report := storage.StorageHealthCheck(ctx, db)
	if !report.FlockProbe.OK {
		t.Errorf("FlockProbe.OK = false for an in-memory database, want true (nothing to flock)")
	}
	if !strings.Contains(report.FlockProbe.Detail, "in-memory") {
		t.Errorf("FlockProbe.Detail = %q, want it to mention in-memory", report.FlockProbe.Detail)
	}
}

// TestStorageHealth_FlockProbe_HeldByAnotherOpen proves the flock-probe
// check detects a currently-held §D-3 exclusive lock: sqlite.Open holds
// the real lock via its own flock_darwin.go/flock_linux.go path, and a
// SEPARATE raw connection's health check against the same file must see
// it as held.
func TestStorageHealth_FlockProbe_HeldByAnotherOpen(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("§D-3 flock is tier-2 (always-refuse) on windows; nothing to hold")
	}
	path := filepath.Join(t.TempDir(), "cascade.db")

	holder, err := sqlite.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("sqlite.Open (lock holder): %v", err)
	}
	defer func() { _ = holder.Close() }()

	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("sql.Open (second connection): %v", err)
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	clock := testkit.NewFrozenClock(time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC))
	if _, err := storage.Bootstrap(ctx, db, storage.BootstrapOpts{Clock: clock}); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}

	report := storage.StorageHealthCheck(ctx, db)
	if report.FlockProbe.OK {
		t.Fatal("FlockProbe.OK = true while sqlite.Open holds the exclusive lock, want false")
	}
	var hcErr *storage.HealthCheckError
	if !errors.As(report.FlockProbe.Err, &hcErr) || hcErr.Check != "flock-probe" {
		t.Errorf("FlockProbe.Err = %v, want *HealthCheckError{Check: flock-probe}", report.FlockProbe.Err)
	}
	if !cascade.HasKind(report.FlockProbe.Err, cascade.KindConflict) {
		t.Errorf("FlockProbe.Err kind: want KindConflict, got %v", report.FlockProbe.Err)
	}
}

// TestStorageHealth_ClosedDBFailsEveryQueryDrivenCheck closes the
// underlying *sql.DB before calling StorageHealthCheck, driving every
// check's raw-query-error branch (distinct from their "row/table absent"
// branches, which the earlier tests already cover) with a real
// database/sql error that is NOT a *mcsqlite.Error — exercising
// classifyProbeError's default KindUnavailable fallback for ProbeWrite,
// and proving HealthReport.OK() correctly reports false once at least one
// check fails.
func TestStorageHealth_ClosedDBFailsEveryQueryDrivenCheck(t *testing.T) {
	db, _ := bootstrappedDB(t)
	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	report := storage.StorageHealthCheck(context.Background(), db)
	if report.OK() {
		t.Fatal("HealthReport.OK() = true against a closed database, want false")
	}
	for name, res := range report.Results() {
		if name == "flock-probe" {
			// The flock probe never touches db itself — it resolves the
			// on-disk path via a query, which also fails closed, but is
			// asserted separately below for its own message.
			continue
		}
		if res.OK {
			t.Errorf("check %q: OK=true against a closed database, want false", name)
		}
	}
	if report.FlockProbe.OK {
		t.Error("FlockProbe.OK = true against a closed database (path resolution must fail closed too), want false")
	}
}
