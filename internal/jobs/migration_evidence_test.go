package jobs

// Purpose: EvidenceMigrationSet/ApplyEvidenceSchema's own contract
//
//	(table presence, idempotent re-apply) plus EvidenceLedger.Verify's
//	R-21.183 chain walk: genesis, a good multi-row chain, a tampered
//	row, and a seq gap -- all over a real modernc-sqlite db in
//	t.TempDir() (Art.2/Art.7.1).
//
// SPORT: jobs/completion-gate/ADD (P1-E29-W6-S60-T3).

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/acamarata/cascade/internal/storage/migrate"
)

func openEvidenceDB(t *testing.T, path string) (*sql.DB, error) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	if err := ApplyJobsSchema(ctx, db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		return nil, err
	}
	if err := ApplyEvidenceSchema(ctx, db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		return nil, err
	}
	return db, nil
}

func openEvidenceDBExisting(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	ctx := context.Background()
	if err := ApplyJobsSchema(ctx, db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		return nil, err
	}
	if err := ApplyEvidenceSchema(ctx, db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		return nil, err
	}
	return db, nil
}

func TestEvidenceMigrationCreatesTableIdempotently(t *testing.T) {
	path := filepath.Join(t.TempDir(), "evidence-mig.db")
	db, err := openEvidenceDB(t, path)
	if err != nil {
		t.Fatalf("first apply: %v", err)
	}
	var name string
	if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, tableEvidence).Scan(&name); err != nil {
		t.Fatalf("jobs_evidence not created: %v", err)
	}
	// Re-apply must be a no-op, never an error.
	if err := ApplyEvidenceSchema(context.Background(), db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		t.Fatalf("re-apply: %v", err)
	}
}

func TestEvidenceHashChain(t *testing.T) {
	l, store, _ := newLedgerFixture(t, time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC))
	ctx := context.Background()

	// Genesis: the first row's prev_hash must be the 32-zero-byte value.
	if _, err := l.Append(ctx, passRecord(EvidenceBuild, "idem-1"), baseAuth()); err != nil {
		t.Fatalf("Append 1: %v", err)
	}
	var prev []byte
	if err := store.db.QueryRow(`SELECT prev_hash FROM ` + tableEvidence + ` WHERE job_id='job-1' AND seq=1`).Scan(&prev); err != nil {
		t.Fatalf("read prev_hash: %v", err)
	}
	if !bytesEqual(prev, genesisHash) {
		t.Fatalf("genesis prev_hash = %x, want 32 zero bytes", prev)
	}

	// A good multi-row chain verifies clean.
	if _, err := l.Append(ctx, passRecord(EvidenceLint, "idem-2"), baseAuth()); err != nil {
		t.Fatalf("Append 2: %v", err)
	}
	if _, err := l.Append(ctx, passRecord(EvidenceTests, "idem-3"), baseAuth()); err != nil {
		t.Fatalf("Append 3: %v", err)
	}
	if err := l.Verify(ctx, "job-1"); err != nil {
		t.Fatalf("Verify on a good chain: %v", err)
	}

	// A tampered row (mutate seq 2's tree_hash after the fact) breaks
	// seq 3's link to it.
	if _, err := store.db.Exec(`UPDATE ` + tableEvidence + ` SET tree_hash='tampered' WHERE job_id='job-1' AND seq=2`); err != nil {
		t.Fatalf("tamper: %v", err)
	}
	if err := l.Verify(ctx, "job-1"); err == nil {
		t.Fatal("Verify on a tampered chain = nil, want ErrEvidenceChainBroken")
	}

	// A seq gap (delete seq 2 entirely) is caught even without tampering
	// seq 3's own stored bytes.
	if _, err := store.db.Exec(`DELETE FROM ` + tableEvidence + ` WHERE job_id='job-1' AND seq=2`); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := l.Verify(ctx, "job-1"); err == nil {
		t.Fatal("Verify with a seq gap = nil, want ErrEvidenceChainBroken")
	}
}
