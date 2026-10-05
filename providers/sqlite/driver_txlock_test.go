// Purpose: proves the Driver's write connection opens its transactions
//
//	IMMEDIATE (P1-BF-R138 D1). A second *sql.DB on the same file, shaped
//	like the daemon's usage-accounting connection (busy_timeout only),
//	writes while a Driver transaction sits between its read and its write.
//	With IMMEDIATE the second writer waits under busy_timeout and both
//	commit; with DEFERRED the Driver's write cannot upgrade its read
//	snapshot and fails "database is locked (517)" at once, which is what a
//	two-leg fan-out hit on a real daemon.
//
// Constraints: t.TempDir only; offline.
package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/provider"
)

func TestSQLiteImmediateWriteTxNoLockedUpgrade(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cascade.db")
	d, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	other, err := sql.Open("sqlite", "file:"+path+"?_busy_timeout=5000")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = other.Close() })
	if _, err := other.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS other_writer (n INTEGER)"); err != nil {
		t.Fatal(err)
	}
	if err := d.Put(ctx, "txlock", "k", []byte("0")); err != nil {
		t.Fatal(err)
	}
	for round := 0; round < 3; round++ {
		otherDone := make(chan error, 1)
		err := d.Tx(ctx, func(txCtx context.Context, tx provider.Tx) error {
			if _, err := tx.Get(txCtx, "txlock", "k"); err != nil {
				return err // the read comes first, as in a create-only CompareAndSwap
			}
			go func() {
				_, e := other.ExecContext(ctx, "INSERT INTO other_writer VALUES (?)", round)
				otherDone <- e
			}()
			select {
			case e := <-otherDone: // DEFERRED: the other write committed inside our read
				otherDone <- e
			case <-time.After(200 * time.Millisecond): // IMMEDIATE: it waits on our lock
			}
			return tx.Put(txCtx, "txlock", "k", []byte{byte('1' + round)})
		})
		if err != nil {
			t.Fatalf("round %d: driver write tx = %v, want a commit (no locked upgrade)", round, err)
		}
		if e := <-otherDone; e != nil {
			t.Fatalf("round %d: second connection write = %v, want a commit after the driver tx", round, e)
		}
	}
	var n int
	if err := other.QueryRowContext(ctx, "SELECT COUNT(*) FROM other_writer").Scan(&n); err != nil || n != 3 {
		t.Fatalf("second connection rows = %d (%v), want 3", n, err)
	}
	if v, err := d.Get(ctx, "txlock", "k"); err != nil || string(v) != "3" {
		t.Fatalf("driver value = %q (%v), want \"3\"", v, err)
	}
}
