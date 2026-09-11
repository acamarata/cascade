// Purpose: SQLiteCapture's consistent-snapshot contract, including the
//
//	§D-15 concurrent-writes case (an uncommitted writer is never visible
//	to a capture, proving VACUUM INTO's snapshot isolation rather than a
//	raw WAL copy) and every typed failure path.
//
// SPORT: internal.backup.capture/ADDED (P1-E19-W4-S41-T1).
package backup

import (
	"bytes"
	"context"
	"database/sql"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
)

var captureTestClock = time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)

func openCaptureTestDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cascade.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	clock := testkit.NewFrozenClock(captureTestClock)
	if _, err := storage.Bootstrap(context.Background(), db, storage.BootstrapOpts{Clock: clock}); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	return db
}

func seedCaptureRow(t *testing.T, db *sql.DB, namespace, key string, value []byte) {
	t.Helper()
	ctx := context.Background()
	const ddl = `CREATE TABLE IF NOT EXISTS kv (
		namespace TEXT NOT NULL,
		key       TEXT NOT NULL,
		value     BLOB NOT NULL,
		PRIMARY KEY (namespace, key)
	) WITHOUT ROWID;`
	if _, err := db.ExecContext(ctx, ddl); err != nil {
		t.Fatalf("seedCaptureRow: create kv table: %v", err)
	}
	_, err := db.ExecContext(ctx,
		`INSERT INTO kv (namespace, key, value) VALUES (?, ?, ?)
			ON CONFLICT (namespace, key) DO UPDATE SET value = excluded.value`,
		namespace, key, value)
	if err != nil {
		t.Fatalf("seedCaptureRow: insert: %v", err)
	}
}

func TestSQLiteCaptureRequiresDB(t *testing.T) {
	c := SQLiteCapture{Domain: storage.DomainContext, Dir: t.TempDir()}
	_, err := c.Export(context.Background())
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("Export(nil DB) error kind = %v, want KindInvalidInput", err)
	}
}

func TestSQLiteCaptureRequiresDir(t *testing.T) {
	db := openCaptureTestDB(t)
	c := SQLiteCapture{DB: db, Domain: storage.DomainContext}
	_, err := c.Export(context.Background())
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("Export(empty Dir) error kind = %v, want KindInvalidInput", err)
	}
}

func TestSQLiteCaptureExportsSeededRow(t *testing.T) {
	db := openCaptureTestDB(t)
	seedCaptureRow(t, db, string(storage.DomainContext), "alpha", []byte("hello capture"))

	c := SQLiteCapture{DB: db, Domain: storage.DomainContext, Dir: t.TempDir()}
	rc, err := c.Export(context.Background())
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	defer func() { _ = rc.Close() }()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read captured stream: %v", err)
	}
	if !bytes.Contains(data, []byte("alpha")) {
		t.Fatalf("captured stream missing seeded row key: %s", data)
	}
}

func TestSQLiteCaptureFailsTypedOnClosedDB(t *testing.T) {
	db := openCaptureTestDB(t)
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	c := SQLiteCapture{DB: db, Domain: storage.DomainContext, Dir: t.TempDir()}
	_, err := c.Export(context.Background())
	if !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("Export(closed DB) error kind = %v, want KindIntegrity", err)
	}
}

// TestCaptureExcludesUncommittedWrite is the §D-15 proof: a torn/dirty read
// never ships. It needs no goroutine and no sleep-based synchronization
// (Art.7.3) — an uncommitted transaction is either visible to a fresh
// snapshot read or it is not, deterministically, by SQLite's own
// transaction-isolation contract, regardless of timing. A raw copy of the
// live WAL file would not offer this guarantee; VACUUM INTO's deferred
// read transaction does.
func TestCaptureExcludesUncommittedWrite(t *testing.T) {
	db := openCaptureTestDB(t)
	domain := storage.DomainContext
	seedCaptureRow(t, db, string(domain), "committed-before", []byte("v1"))

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	_, err = tx.Exec(
		`INSERT INTO kv (namespace, key, value) VALUES (?, ?, ?)`,
		string(domain), "uncommitted-during", []byte("v2"))
	if err != nil {
		t.Fatalf("tx.Exec: %v", err)
	}

	c := SQLiteCapture{DB: db, Domain: domain, Dir: t.TempDir()}
	rc, err := c.Export(context.Background())
	if err != nil {
		t.Fatalf("Export while a write transaction is open: %v", err)
	}
	duringData, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		t.Fatalf("read captured stream: %v", err)
	}
	if bytes.Contains(duringData, []byte("uncommitted-during")) {
		t.Fatal("capture observed an UNCOMMITTED write — torn/dirty snapshot")
	}
	if !bytes.Contains(duringData, []byte("committed-before")) {
		t.Fatal("capture is missing a row that was committed before the transaction opened")
	}

	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	rc2, err := SQLiteCapture{DB: db, Domain: domain, Dir: t.TempDir()}.Export(context.Background())
	if err != nil {
		t.Fatalf("Export after commit: %v", err)
	}
	afterData, err := io.ReadAll(rc2)
	_ = rc2.Close()
	if err != nil {
		t.Fatalf("read captured stream: %v", err)
	}
	if !bytes.Contains(afterData, []byte("uncommitted-during")) {
		t.Fatal("capture after commit is missing the now-committed row")
	}
}
