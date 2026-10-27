// Purpose: edge cases of OpenReadOnlyStore: an empty lone -wal and a store
//
//	with tables but no kv table (split from readonly_store_test.go for the
//	300-line cap).
//
// SPORT: internal/runtime OpenReadOnlyStore/ADDED (P1-SEC-12 tests).
package runtime_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	_ "modernc.org/sqlite"
)

// TestOpenReadOnlyStoreEmptyLoneWALCountsAsAbsent: a zero-length -wal holds no
// frames, so it must not turn a readable store into a "try again".
func TestOpenReadOnlyStoreEmptyLoneWALCountsAsAbsent(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "cascade.db")
	seed := seedStore(t, dbPath)
	if err := seed.Close(); err != nil {
		t.Fatalf("seed Close: %v", err)
	}
	if err := os.WriteFile(dbPath+"-wal", nil, 0o600); err != nil {
		t.Fatalf("write an empty wal: %v", err)
	}
	before := dirDigest(t, dir)
	store, closeFn, err := runtime.OpenReadOnlyStore(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("an empty lone -wal must not block the open: %v", err)
	}
	if got, gerr := store.Get(context.Background(), "ns", "alpha"); gerr != nil || string(got) != "one" {
		t.Fatalf("Get = %q, %v; want one", got, gerr)
	}
	_ = closeFn()
	if dirDigest(t, dir) != before {
		t.Fatal("the open changed the directory")
	}
}

// TestOpenReadOnlyStoreTablesWithoutKVIsAnIntegrityError: other tables but no
// kv table is not a creation race, so it is not an in-transition refusal.
func TestOpenReadOnlyStoreTablesWithoutKVIsAnIntegrityError(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "cascade.db")
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE other (x INTEGER)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	_ = db.Close()
	for _, s := range []string{"-wal", "-shm"} {
		_ = os.Remove(dbPath + s)
	}
	_, _, oerr := runtime.OpenReadOnlyStore(context.Background(), dbPath)
	if oerr == nil || runtime.IsStoreInTransition(oerr) {
		t.Fatalf("tables without kv: %v, want a plain integrity error", oerr)
	}
	if kind, ok := cascade.KindOf(oerr); !ok || kind != cascade.KindIntegrity {
		t.Fatalf("tables without kv: %v, want KindIntegrity", oerr)
	}
}
