package topology

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver

	"github.com/acamarata/cascade/internal/storage/migrate"
)

// fakeClock is a fixed migrate.Clock for tests (Art.7.3 -- no bare time.Now).
type fakeClock struct{ t time.Time }

func (c fakeClock) Now() time.Time { return c.t }

func newTestClock() fakeClock { return fakeClock{t: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)} }

// openRealSQLiteFile opens a REAL, file-backed modernc SQLite database
// under t.TempDir (Art.2.1/2.2's "real counterpart" rule -- never an
// in-memory-only stand-in for this ticket's migration proof).
func openRealSQLiteFile(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "topology.db")
	db, err := sql.Open("sqlite", "file:"+path+"?_journal_mode=WAL&_foreign_keys=1")
	if err != nil {
		t.Fatalf("open real sqlite file: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestMigrationRealSQLite applies MigrationSet against a REAL modernc
// SQLite database on a t.TempDir file, then re-applies it to prove
// idempotence, per Art.2.1.
func TestMigrationRealSQLite(t *testing.T) {
	db := openRealSQLiteFile(t)
	ctx := context.Background()
	dialect := migrate.SQLiteEmitter{}
	clock := newTestClock()

	if err := ApplyMigrationSchema(ctx, db, dialect, clock, "", ""); err != nil {
		t.Fatalf("first ApplyMigrationSchema on real sqlite file: %v", err)
	}
	for _, table := range []string{tableAccount, tableCredential, tableQuotaDomain, tableRuntimeProfile, tableLane} {
		var name string
		err := db.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name)
		if err != nil {
			t.Errorf("table %s not created: %v", table, err)
		}
	}
	// R-14.5/R-16.51/R-16.75 domain list stays unchanged: this ticket never
	// registers a new DomainID, it only adds tables to the existing
	// `config` domain (R-21.22) -- there is nothing to assert against
	// storage.AllDomains because this package never calls it (see this
	// ticket's journal for the full contract-vs-tree note).

	if err := ApplyMigrationSchema(ctx, db, dialect, clock, "", ""); err != nil {
		t.Fatalf("second ApplyMigrationSchema (re-apply) should be a no-op, got: %v", err)
	}
}

func TestApplyMigrationSchemaRequiresDBAndClock(t *testing.T) {
	ctx := context.Background()
	if err := ApplyMigrationSchema(ctx, nil, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err == nil {
		t.Fatal("ApplyMigrationSchema(nil db, ...) should have failed")
	}
	db := openRealSQLiteFile(t)
	if err := ApplyMigrationSchema(ctx, db, migrate.SQLiteEmitter{}, nil, "", ""); err == nil {
		t.Fatal("ApplyMigrationSchema(..., nil clock, ...) should have failed")
	}
}

// TestMigrationSetPostgresEmitSucceeds proves MigrationSet emits valid DDL
// under the Postgres dialect too (no live Postgres in this sandbox).
func TestMigrationSetPostgresEmitSucceeds(t *testing.T) {
	if _, err := (migrate.PostgresEmitter{}).Emit(MigrationSet()); err != nil {
		t.Fatalf("PostgresEmitter.Emit: %v", err)
	}
}

func TestMigrationSetIdentity(t *testing.T) {
	set := MigrationSet()
	if set.SetID != "fleet-topology" {
		t.Errorf("SetID = %q, want \"fleet-topology\"", set.SetID)
	}
	if set.SchemaVersion != 2 || set.ReaderCeiling != 2 || SchemaVersion != 2 {
		t.Errorf("SchemaVersion/ReaderCeiling = %d/%d, want 2/2", set.SchemaVersion, set.ReaderCeiling)
	}
	if len(set.Steps) != 6 {
		t.Fatalf("expected 6 migration steps (v1's five tables plus billing evidence), got %d", len(set.Steps))
	}
	// v2 only appends: the five v1 steps keep their exact definitions.
	v1 := []string{tableAccount, tableQuotaDomain, tableRuntimeProfile, tableCredential, tableLane}
	for i, name := range v1 {
		if set.Steps[i].Kind != migrate.StepCreateTable || set.Steps[i].Table.Name != name {
			t.Errorf("step %d = %v %q, want create %q", i, set.Steps[i].Kind, set.Steps[i].Table.Name, name)
		}
	}
	if last := set.Steps[5]; last.Kind != migrate.StepCreateTable || last.Table.Name != tableBillingEvidence {
		t.Errorf("step 5 must create %q", tableBillingEvidence)
	}
}

// migrationSetV1 rebuilds the shipped v1 set: the first five steps at
// SchemaVersion 1.
func migrationSetV1() migrate.MigrationSet {
	set := MigrationSet()
	set.SchemaVersion, set.ReaderCeiling, set.Steps = 1, 1, set.Steps[:5]
	return set
}

func TestMigrationV2AddsBillingEvidenceTable(t *testing.T) {
	isolatedRoot(t)
	db := openRealSQLiteFile(t)
	ctx := context.Background()
	cfg := migrate.ApplyConfig{DB: db, Dialect: migrate.SQLiteEmitter{}, Clock: newTestClock()}
	if err := migrate.Apply(ctx, cfg, migrationSetV1()); err != nil {
		t.Fatalf("apply v1: %v", err)
	}
	var n int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE name = ?`, tableBillingEvidence).Scan(&n); err != nil || n != 0 {
		t.Fatalf("v1 already has %s: n=%d err=%v", tableBillingEvidence, n, err)
	}
	s := NewStore(db)
	acct := Account{ID: "acct-v1", Provider: "prov-v1", Billing: BillingInfo{Kind: BillingAPI}, Role: AccountRoleWorkforce}
	dom := QuotaDomain{ID: "dom-v1", AccountRef: acct.ID, Kind: QuotaDomainAPIProject, BillingTier: BillingTierDiscover}
	if err := s.UpsertAccount(ctx, acct); err != nil {
		t.Fatalf("v1 account: %v", err)
	}
	if err := s.UpsertQuotaDomain(ctx, dom); err != nil {
		t.Fatalf("v1 domain: %v", err)
	}
	for i := 0; i < 2; i++ { // the second v2 apply is a no-op
		if err := ApplyMigrationSchema(ctx, db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
			t.Fatalf("apply v2 (pass %d): %v", i, err)
		}
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE name = ?`, tableBillingEvidence).Scan(&n); err != nil || n != 1 {
		t.Fatalf("v2 lacks %s: n=%d err=%v", tableBillingEvidence, n, err)
	}
	got, err := s.GetQuotaDomain(ctx, "dom-v1")
	if err != nil || got.AccountRef != dom.AccountRef || got.BillingTier != BillingTierDiscover || got.Kind != dom.Kind {
		t.Fatalf("v1 domain after v2: %+v, %v", got, err)
	}
	if a, err := s.GetAccount(ctx, "acct-v1"); err != nil || a != acct {
		t.Fatalf("v1 account after v2: %+v, %v", a, err)
	}
	ev, err := s.BillingEvidenceFor(ctx, "dom-v1")
	if err != nil || len(ev) != 0 {
		t.Fatalf("v2 evidence on v1 domain: %d, %v", len(ev), err)
	}
	// Every v2 column is NOT NULL and domain_id references a real domain.
	if _, err := db.ExecContext(ctx, `INSERT INTO `+tableBillingEvidence+` (domain_id) VALUES ('dom-v1')`); err == nil {
		t.Fatal("a row with NULL columns was accepted")
	}
	if err := s.insertEvidence(ctx, passingEvidence("ghost", "a")); err == nil {
		t.Fatal("an evidence row for an unknown domain was accepted")
	}
	if err := s.insertEvidence(ctx, passingEvidence("dom-v1", "a")); err != nil {
		t.Fatalf("valid evidence row: %v", err)
	}
}
