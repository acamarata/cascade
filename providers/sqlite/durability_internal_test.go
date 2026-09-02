// Purpose: proves the CR fix 3 durability pin ("_synchronous=FULL" in
//
//	openLocked's DSN) is actually in effect on the Driver's live write
//	connection, not merely assumed from modernc-sqlite's compiled-in
//	default — the whole point of the fix is that nothing previously
//	asserted this, so a silent regression would have gone unnoticed.
//	White-box (package sqlite, not sqlite_test) because reading the live
//	PRAGMA value requires the unexported writeDB field; no exported
//	surface for this exists and none should be added just for a test.
//
// SPORT: providers.sqlite.Driver/CHANGED (P1-E02-W1-S02-T2 CR fix).
package sqlite

import (
	"context"
	"path/filepath"
	"testing"
)

// sqliteSynchronousFull is PRAGMA synchronous's documented integer value
// for FULL (0=OFF, 1=NORMAL, 2=FULL, 3=EXTRA) — see sqlite.org/pragma.html
// #pragma_synchronous.
const sqliteSynchronousFull = 2

// TestOpenLocked_PinsSynchronousFull opens a real Driver and reads back
// PRAGMA synchronous on its actual write connection, proving the DSN's
// "_synchronous=FULL" param took effect at runtime rather than merely
// being present in the connection string.
func TestOpenLocked_PinsSynchronousFull(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cascade.db")
	d, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})

	var mode int
	row := d.writeDB.QueryRowContext(context.Background(), "PRAGMA synchronous;")
	if err := row.Scan(&mode); err != nil {
		t.Fatalf("PRAGMA synchronous: %v", err)
	}
	if mode != sqliteSynchronousFull {
		t.Fatalf("PRAGMA synchronous = %d, want %d (FULL) — the durability pin is not in effect", mode, sqliteSynchronousFull)
	}
}
