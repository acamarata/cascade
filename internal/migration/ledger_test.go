// Purpose: LedgerStore's contract over a REAL B-layer store —
// providers/sqlite on a t.TempDir() file, matching
// internal/policy/ledger_test.go's own established convention for a kv
// ledger row inside the `audit` domain.
// SPORT: internal.migration.LedgerStore/ADD (P1-E26-W10-S53-T4).
package migration

import (
	"context"
	"testing"
	"time"

	migrationv1 "github.com/acamarata/cascade/internal/migration/v1"
	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/providers/sqlite"
)

func newTestLedger(t *testing.T) *LedgerStore {
	t.Helper()
	db, err := sqlite.Open(context.Background(), t.TempDir()+"/cascade.db")
	if err != nil {
		t.Fatalf("opening the real SQLite store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	l, err := NewLedgerStore(db, testkit.NewFrozenClock(time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC)))
	if err != nil {
		t.Fatalf("NewLedgerStore: %v", err)
	}
	return l
}

// TestLedgerReadDomainNotFound proves an unwritten domain reads back as
// "no row", not an error.
func TestLedgerReadDomainNotFound(t *testing.T) {
	l := newTestLedger(t)
	_, found, err := l.ReadDomain(context.Background(), migrationv1.DomainConfig)
	if err != nil {
		t.Fatalf("ReadDomain on an empty ledger: %v", err)
	}
	if found {
		t.Fatal("ReadDomain reported a row that was never written")
	}
}

// TestLedgerWriteThenReadRoundTrips proves a written row reads back with
// the same fields the clock and the caller supplied, and that the
// namespace it lands in is the `audit` domain (R-16.65).
func TestLedgerWriteThenReadRoundTrips(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.Open(ctx, t.TempDir()+"/cascade.db")
	if err != nil {
		t.Fatalf("opening the real SQLite store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	frozen := testkit.NewFrozenClock(time.Date(2026, 9, 13, 1, 2, 3, 0, time.UTC))
	l, err := NewLedgerStore(db, frozen)
	if err != nil {
		t.Fatalf("NewLedgerStore: %v", err)
	}

	if err := l.WriteDomain(ctx, LedgerRow{
		Domain: migrationv1.DomainVault, Status: StatusDone, RecordCount: 3,
	}); err != nil {
		t.Fatalf("WriteDomain: %v", err)
	}

	row, found, err := l.ReadDomain(ctx, migrationv1.DomainVault)
	if err != nil || !found {
		t.Fatalf("ReadDomain after WriteDomain: row=%+v found=%v err=%v", row, found, err)
	}
	if row.Status != StatusDone || row.RecordCount != 3 {
		t.Fatalf("round trip changed the row: %+v", row)
	}
	if row.ImportedAtUnixNano != frozen.Now().UTC().UnixNano() {
		t.Fatalf("ImportedAtUnixNano was not stamped from the injected clock: %+v", row)
	}
	if !row.Done() {
		t.Fatal("Done() is false for a StatusDone row")
	}

	stored, err := db.Get(ctx, LedgerNamespace, ledgerKey(migrationv1.DomainVault))
	if err != nil || len(stored) == 0 {
		t.Fatalf("the row was not found in the audit domain namespace: %v", err)
	}
	if LedgerNamespace != string(storage.DomainAudit) {
		t.Fatalf("LedgerNamespace = %q, want the audit domain", LedgerNamespace)
	}
}

// TestLedgerWriteDomainOverwritesErrorWithDone proves a ledger row is
// overwritable (unconditional Put), unlike a single-use approval nonce:
// a domain that errored on one run must be able to move to StatusDone on
// a later retry.
func TestLedgerWriteDomainOverwritesErrorWithDone(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)

	if err := l.WriteDomain(ctx, LedgerRow{Domain: migrationv1.DomainMemory, Status: StatusError, Error: "boom"}); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := l.WriteDomain(ctx, LedgerRow{Domain: migrationv1.DomainMemory, Status: StatusDone, RecordCount: 5}); err != nil {
		t.Fatalf("overwriting write: %v", err)
	}
	row, found, err := l.ReadDomain(ctx, migrationv1.DomainMemory)
	if err != nil || !found || row.Status != StatusDone || row.Error != "" {
		t.Fatalf("overwrite did not converge to StatusDone: %+v found=%v err=%v", row, found, err)
	}
}

// TestLedgerWriteDomainRefusesInvalidRow proves WriteDomain validates
// before it stores.
func TestLedgerWriteDomainRefusesInvalidRow(t *testing.T) {
	l := newTestLedger(t)
	err := l.WriteDomain(context.Background(), LedgerRow{Domain: "bogus", Status: StatusDone})
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("an invalid row was not refused with KindInvalidInput: %v", err)
	}
}

// TestNewLedgerStoreRequiresStoreAndClock proves the constructor fails
// closed on either missing dependency.
func TestNewLedgerStoreRequiresStoreAndClock(t *testing.T) {
	db, err := sqlite.Open(context.Background(), t.TempDir()+"/cascade.db")
	if err != nil {
		t.Fatalf("opening the real SQLite store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := NewLedgerStore(nil, testkit.NewFrozenClock(time.Now())); err == nil {
		t.Fatal("nil store was accepted")
	}
	if _, err := NewLedgerStore(db, nil); err == nil {
		t.Fatal("nil clock was accepted")
	}
}

// TestLedgerNilReceiverRefuses proves a nil *LedgerStore fails closed
// rather than panicking, matching internal/policy.Ledger's own nil-safety.
func TestLedgerNilReceiverRefuses(t *testing.T) {
	var l *LedgerStore
	if _, _, err := l.ReadDomain(context.Background(), migrationv1.DomainConfig); err == nil {
		t.Fatal("nil ledger ReadDomain did not refuse")
	}
	if err := l.WriteDomain(context.Background(), LedgerRow{Domain: migrationv1.DomainConfig, Status: StatusDone}); err == nil {
		t.Fatal("nil ledger WriteDomain did not refuse")
	}
}
