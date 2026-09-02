// Purpose: proves the CR fix 2 error-taxonomy mapping end-to-end through
//
//	the PUBLIC Open API — the two real-world failure modes a caller can
//	trigger just by pointing Open at a bad file, without needing a raw
//	connection: a corrupt/non-database file (KindIntegrity) and a
//	read-only database file (KindPermissionDenied). The codes that need a
//	raw connection to trigger for real (SQLITE_CONSTRAINT, SQLITE_BUSY)
//	are covered in errors_internal_test.go instead. See
//	providers/sqlite/README.md "Error taxonomy mapping" for the full code
//	table and which codes have no real trigger at all.
//
// SPORT: providers.sqlite.Driver/CHANGED (P1-E02-W1-S02-T2 CR fix).
package sqlite_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/providers/sqlite"
)

// TestOpen_CorruptDatabaseFile proves that opening a file that is not a
// valid SQLite database at all — the simplest real way to produce
// SQLITE_CORRUPT/SQLITE_NOTADB — surfaces as KindIntegrity, not the
// generic KindUnavailable a caller's retry middleware would otherwise
// spin forever on.
func TestOpen_CorruptDatabaseFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corrupt.db")
	if err := os.WriteFile(path, []byte("this is not a sqlite database, just garbage bytes\x00\x01\x02"), 0o644); err != nil {
		t.Fatalf("os.WriteFile: %v", err)
	}

	_, err := sqlite.Open(context.Background(), path)
	if err == nil {
		t.Fatal("Open on a garbage-bytes file: want an error, got nil")
	}
	if !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("Open on a garbage-bytes file: want KindIntegrity, got %v", err)
	}
}

// TestOpen_ReadonlyDatabaseFile proves that Put against an already-valid
// database file that has since become read-only surfaces as
// KindPermissionDenied. openLocked's schemaDDL ("CREATE TABLE IF NOT
// EXISTS") is a logical no-op against a database whose schema already
// exists, and modernc-sqlite does not take a write lock to execute it in
// that case — so Open itself still succeeds against a 0o444 file; the
// first real write attempt (Put) is what surfaces SQLITE_READONLY.
func TestOpen_ReadonlyDatabaseFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ro.db")
	ctx := context.Background()

	first, err := sqlite.Open(ctx, path)
	if err != nil {
		t.Fatalf("first Open (create): %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("first.Close: %v", err)
	}

	if err := os.Chmod(path, 0o444); err != nil {
		t.Fatalf("os.Chmod(0o444): %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) }) // let t.TempDir() clean up

	d, err := sqlite.Open(ctx, path)
	if err != nil {
		t.Fatalf("second Open against a 0o444 database file: want success (schema is a no-op, no write needed yet), got %v", err)
	}
	defer func() {
		if err := d.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()

	err = d.Put(ctx, "ns", "k", []byte("v"))
	if err == nil {
		t.Fatal("Put against a 0o444 database file: want an error, got nil")
	}
	if !cascade.HasKind(err, cascade.KindPermissionDenied) {
		t.Fatalf("Put against a 0o444 database file: want KindPermissionDenied, got %v", err)
	}
}
