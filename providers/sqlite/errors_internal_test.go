// Purpose: unit-tests classifyDBError (errors.go) directly against REAL
//
//	errors produced by the real modernc-sqlite driver (Art.2 — no
//	fabricated error values), for the codes Driver's own public write
//	path cannot produce by construction (SQLITE_CONSTRAINT: Put always
//	upserts via ON CONFLICT, so a real constraint violation can only be
//	raised through a raw connection issuing a plain INSERT) plus the
//	fallback branches. Codes classifyDBError DOES reach through the
//	public Driver/Open API (KindIntegrity via a corrupt file,
//	KindPermissionDenied via a readonly file) are instead proven
//	end-to-end in errors_test.go (package sqlite_test) against Open
//	itself — this file only covers what the public surface cannot reach.
//	White-box (package sqlite) because it calls the unexported
//	classifyDBError directly.
//
// SPORT: providers.sqlite.Driver/CHANGED (P1-E02-W1-S02-T2 CR fix).
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestClassifyDBError_NonSQLiteError proves the documented fallback: an
// error that does not wrap a *modernc.org/sqlite.Error at all (never
// actually possible to fabricate as a real SQLite result code, since it
// isn't one) classifies as KindUnavailable, the pre-fix default this
// function narrows rather than replaces.
func TestClassifyDBError_NonSQLiteError(t *testing.T) {
	if got := classifyDBError(errors.New("not a sqlite error")); got != cascade.KindUnavailable {
		t.Fatalf("classifyDBError(generic error) = %v, want KindUnavailable", got)
	}
	if got := classifyDBError(nil); got != cascade.KindUnavailable {
		t.Fatalf("classifyDBError(nil) = %v, want KindUnavailable", got)
	}
}

// TestClassifyDBError_RealConstraintViolation triggers a genuine
// SQLITE_CONSTRAINT by issuing a raw duplicate-primary-key INSERT (no ON
// CONFLICT clause) against a real modernc-sqlite connection — the one
// taxonomy code Driver's own Put/Delete/CompareAndSwap surface cannot
// produce, since Put always upserts.
func TestClassifyDBError_RealConstraintViolation(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "raw.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.ExecContext(ctx, `CREATE TABLE t (k TEXT PRIMARY KEY)`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO t (k) VALUES ('a')`); err != nil {
		t.Fatalf("first INSERT: %v", err)
	}

	_, err = db.ExecContext(ctx, `INSERT INTO t (k) VALUES ('a')`) // real duplicate PK
	if err == nil {
		t.Fatal("duplicate primary key INSERT: want a real SQLITE_CONSTRAINT error, got nil")
	}
	if got := classifyDBError(err); got != cascade.KindConflict {
		t.Fatalf("classifyDBError(real SQLITE_CONSTRAINT) = %v, want KindConflict (err: %v)", got, err)
	}
}

// TestClassifyDBError_RealBusy triggers a genuine SQLITE_BUSY by holding a
// write lock open on one connection while a second, independent
// connection (a short _busy_timeout so the test stays fast) attempts to
// write concurrently. This deliberately bypasses providers/sqlite's own
// single-write-connection executor (which would simply serialize the two
// writes without contention) — the point is to prove classifyDBError's
// SQLITE_BUSY branch against the real result code SQLite raises when two
// independent OS connections genuinely contend, which is exactly the
// error database/sql could hand a Driver call if this package's own
// MaxOpenConns(1) invariant were ever violated.
func TestClassifyDBError_RealBusy(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "busy.db")

	holder, err := sql.Open("sqlite", "file:"+path+"?_journal_mode=WAL")
	if err != nil {
		t.Fatalf("sql.Open(holder): %v", err)
	}
	t.Cleanup(func() { _ = holder.Close() })
	holder.SetMaxOpenConns(1)
	if _, err := holder.ExecContext(ctx, `CREATE TABLE t (k TEXT)`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}

	tx, err := holder.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	// A write inside the transaction forces SQLite to take the RESERVED
	// (WAL: writer) lock immediately rather than leaving it deferred.
	if _, err := tx.ExecContext(ctx, `INSERT INTO t (k) VALUES ('holder')`); err != nil {
		t.Fatalf("INSERT inside holder tx: %v", err)
	}

	contender, err := sql.Open("sqlite", "file:"+path+"?_journal_mode=WAL&_busy_timeout=100")
	if err != nil {
		t.Fatalf("sql.Open(contender): %v", err)
	}
	t.Cleanup(func() { _ = contender.Close() })
	contender.SetMaxOpenConns(1)

	start := time.Now()
	_, err = contender.ExecContext(ctx, `INSERT INTO t (k) VALUES ('contender')`)
	if err == nil {
		t.Fatal("contending write while holder tx is open and uncommitted: want a real SQLITE_BUSY error, got nil")
	}
	if elapsed := time.Since(start); elapsed < 50*time.Millisecond {
		t.Fatalf("contending write returned in %v, suspiciously fast for a genuine busy-timeout wait", elapsed)
	}
	if got := classifyDBError(err); got != cascade.KindUnavailable {
		t.Fatalf("classifyDBError(real SQLITE_BUSY) = %v, want KindUnavailable (err: %v)", got, err)
	}
}

// TestClassifyDBError_RealReadonly triggers a genuine SQLITE_READONLY by
// chmod-ing a database file to read-only after its schema already exists,
// then attempting a schema-modifying statement against it — mirroring
// what openLocked's own schemaDDL exec would hit against a readonly file.
func TestClassifyDBError_RealReadonly(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ro.db")

	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE t (k TEXT)`); err != nil {
		_ = db.Close()
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close (before chmod): %v", err)
	}

	if err := os.Chmod(path, 0o444); err != nil {
		t.Fatalf("os.Chmod(0o444): %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) }) // let t.TempDir() clean up

	roDB, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("sql.Open(readonly file): %v", err)
	}
	t.Cleanup(func() { _ = roDB.Close() })

	_, err = roDB.ExecContext(ctx, `CREATE TABLE t2 (k TEXT)`)
	if err == nil {
		t.Fatal("schema-modifying statement against a 0o444 database file: want a real SQLITE_READONLY error, got nil")
	}
	if got := classifyDBError(err); got != cascade.KindPermissionDenied {
		t.Fatalf("classifyDBError(real SQLITE_READONLY) = %v, want KindPermissionDenied (err: %v)", got, err)
	}
}
