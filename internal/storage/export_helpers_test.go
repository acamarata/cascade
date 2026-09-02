package storage_test

// Purpose: shared test helpers for the export_*_test.go split (mirrors
//
//	domains_helpers_test.go's own role for the domains_*_test.go split):
//	bootstrapping a real modernc-sqlite database in t.TempDir() (Art.7.1),
//	seeding kv rows directly via raw SQL (bypassing Import — these tests
//	need to construct "already there" state independently of the code
//	under test), and reading kv rows back for assertions (Art.2 — every
//	assertion here queries the real database, never trusts a return value
//	alone).
//
// SPORT: internal.storage.export.Export/ADDED,
//
//	internal.storage.export.Import/ADDED (P1-E02-W1-S03-T3).

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/internal/testkit"
)

// exportTestClock is the fixed instant every determinism-sensitive test in
// this split freezes exportClock to, via storage.SetExportClock.
var exportTestClock = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

// bootstrappedTestDB opens a real modernc-sqlite database in t.TempDir()
// and runs storage.Bootstrap against it with a frozen clock, so every
// export_*_test.go test starts from a database with the applied_migrations
// schema_version stamp (health.go/domains.go's floor) already present —
// exactly what Export/Import require per readSchemaVersion's contract.
func bootstrappedTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db := openTestDB(t)
	clock := testkit.NewFrozenClock(exportTestClock)
	if _, err := storage.Bootstrap(context.Background(), db, storage.BootstrapOpts{Clock: clock}); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	return db
}

// seedKVRow inserts (namespace, key, value) directly into the shared kv
// table via raw SQL, creating the table first if absent — a setup path
// deliberately independent of Import, so round-trip/version-guard/
// collision tests are not circularly validated by the same code they
// exercise.
func seedKVRow(t *testing.T, db *sql.DB, namespace, key string, value []byte) {
	t.Helper()
	ctx := context.Background()
	const ddl = `CREATE TABLE IF NOT EXISTS kv (
		namespace TEXT NOT NULL,
		key       TEXT NOT NULL,
		value     BLOB NOT NULL,
		PRIMARY KEY (namespace, key)
	) WITHOUT ROWID;`
	if _, err := db.ExecContext(ctx, ddl); err != nil {
		t.Fatalf("seedKVRow: create kv table: %v", err)
	}
	_, err := db.ExecContext(ctx,
		`INSERT INTO kv (namespace, key, value) VALUES (?, ?, ?)
			ON CONFLICT (namespace, key) DO UPDATE SET value = excluded.value`,
		namespace, key, value,
	)
	if err != nil {
		t.Fatalf("seedKVRow: insert %s/%s: %v", namespace, key, err)
	}
}

// readKVRow reads one (namespace, key)'s value directly, reporting whether
// it exists — the Art.2 "direct SELECT after Import" ground truth the
// round-trip and conflict-strategy acceptance criteria call for.
func readKVRow(t *testing.T, db *sql.DB, namespace, key string) (value []byte, exists bool) {
	t.Helper()
	var v []byte
	err := db.QueryRowContext(context.Background(),
		`SELECT value FROM kv WHERE namespace = ? AND key = ?`, namespace, key,
	).Scan(&v)
	switch err {
	case nil:
		return v, true
	case sql.ErrNoRows:
		return nil, false
	default:
		t.Fatalf("readKVRow: select %s/%s: %v", namespace, key, err)
		return nil, false
	}
}

// countKVRows reports how many rows exist under namespace — used to prove
// "zero rows written" after every refused-import test case. A missing kv
// table (never-written domain) counts as zero, not an error.
func countKVRows(t *testing.T, db *sql.DB, namespace string) int {
	t.Helper()
	exists, err := kvTableExistsForTest(db)
	if err != nil {
		t.Fatalf("countKVRows: check kv table: %v", err)
	}
	if !exists {
		return 0
	}
	var n int
	err = db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM kv WHERE namespace = ?`, namespace,
	).Scan(&n)
	if err != nil {
		t.Fatalf("countKVRows: count %s: %v", namespace, err)
	}
	return n
}

// kvTableExistsForTest is countKVRows's own sqlite_master probe — a small,
// deliberate duplicate of storage's unexported kvTableExists rather than a
// reach into the package under test (this file is package storage_test,
// black-box, by the established convention every other export_*_test.go/
// domains_*_test.go file in this package follows).
func kvTableExistsForTest(db *sql.DB) (bool, error) {
	var name string
	err := db.QueryRowContext(context.Background(),
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'kv'`,
	).Scan(&name)
	switch err {
	case nil:
		return true, nil
	case sql.ErrNoRows:
		return false, nil
	default:
		return false, err
	}
}

// exportGoldenPath is the contract-mandated golden fixture path
// (T-3.yaml files_scope), a literal ticket requirement distinct from
// internal/testkit.Golden's own "testdata/goldens/<name>.golden"
// convention — so this helper reimplements testkit.Golden's compare/
// update contract (same CASCADE_TESTKIT_UPDATE_GOLDEN env var via
// testkit.UpdateRequested, same CI guard) against the exact path the
// ticket names, rather than reusing testkit.Golden itself.
const exportGoldenPath = "testdata/golden/export-seed.jsonl"

// compareOrUpdateExportGolden compares got against exportGoldenPath. In
// update mode (CASCADE_TESTKIT_UPDATE_GOLDEN=1, never under CI — see
// testkit.Golden's own doc, mirrored here) it writes got to that path
// instead of comparing.
func compareOrUpdateExportGolden(t *testing.T, got []byte) []byte {
	t.Helper()
	if testkit.UpdateRequested() {
		if os.Getenv("CI") != "" {
			t.Fatalf("export_test: refusing to update golden %q: CASCADE_TESTKIT_UPDATE_GOLDEN=1 was set but so was CI — goldens are never regenerated in CI", exportGoldenPath)
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(exportGoldenPath), 0o755); err != nil {
			t.Fatalf("export_test: creating golden dir: %v", err)
			return nil
		}
		if err := os.WriteFile(exportGoldenPath, got, 0o644); err != nil {
			t.Fatalf("export_test: writing golden: %v", err)
			return nil
		}
		t.Logf("export_test: updated golden %q (CASCADE_TESTKIT_UPDATE_GOLDEN=1)", exportGoldenPath)
		return got
	}

	want, err := os.ReadFile(exportGoldenPath)
	if err != nil {
		t.Fatalf("export_test: reading golden %q (set CASCADE_TESTKIT_UPDATE_GOLDEN=1 locally, never in CI, to create it): %v", exportGoldenPath, err)
		return nil
	}
	if !bytes.Equal(want, got) {
		t.Errorf("export_test: golden mismatch for %q\n--- want ---\n%s\n--- got ---\n%s", exportGoldenPath, want, got)
	}
	return want
}
