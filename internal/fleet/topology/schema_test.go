package topology

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestTableDefsHaveExpectedNames pins the five table names this package
// creates inside the `config` storage domain (R-21.22): each is prefixed
// "config_", the domain's TablePrefix.
func TestTableDefsHaveExpectedNames(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		{accountTable().Name, tableAccount},
		{quotaDomainTable().Name, tableQuotaDomain},
		{runtimeProfileTable().Name, tableRuntimeProfile},
		{credentialTable().Name, tableCredential},
		{laneTable().Name, tableLane},
	}
	for _, c := range cases {
		if c.name != c.want {
			t.Errorf("table name = %q, want %q", c.name, c.want)
		}
	}
	for _, n := range []string{tableAccount, tableQuotaDomain, tableRuntimeProfile, tableCredential, tableLane} {
		if len(n) < 7 || n[:7] != "config_" {
			t.Errorf("table %q should be prefixed config_ (the `config` domain's TablePrefix, R-21.22)", n)
		}
	}
}

func TestTableDefsHaveColumns(t *testing.T) {
	tables := map[string]int{
		"account":         len(accountTable().Columns),
		"quota_domain":    len(quotaDomainTable().Columns),
		"runtime_profile": len(runtimeProfileTable().Columns),
		"credential":      len(credentialTable().Columns),
		"lane":            len(laneTable().Columns),
	}
	for name, n := range tables {
		if n == 0 {
			t.Errorf("table %s should declare columns", name)
		}
	}
}

func TestLaneTableForeignKeysReferenceRealTables(t *testing.T) {
	fks := laneTable().ForeignKeys
	if len(fks) != 3 {
		t.Fatalf("lane table should declare 3 foreign keys, got %d", len(fks))
	}
	want := map[string]string{"runtime_profile_ref": tableRuntimeProfile, "quota_domain_ref": tableQuotaDomain, "credential_ref": tableCredential}
	for _, fk := range fks {
		refTable, ok := want[fk.Column]
		if !ok {
			t.Errorf("unexpected FK column %q", fk.Column)
			continue
		}
		if fk.RefTable != refTable {
			t.Errorf("FK %s references %q, want %q", fk.Column, fk.RefTable, refTable)
		}
	}
}
func TestClosedDBExecErrorPaths(t *testing.T) {
	ctx := context.Background()
	db := openRealSQLiteFile(t)
	if err := ApplyMigrationSchema(ctx, db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		t.Fatalf("ApplyMigrationSchema: %v", err)
	}
	store := NewStore(db)
	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	if err := store.UpsertAccount(ctx, Account{ID: "a", Billing: BillingInfo{Kind: BillingAPI}, Role: AccountRoleWorkforce}); err == nil {
		t.Error("UpsertAccount on a closed db should fail")
	}
	if err := store.UpsertQuotaDomain(ctx, QuotaDomain{ID: "d", Kind: QuotaDomainAPIProject, BillingTier: BillingTierDiscover}); err == nil {
		t.Error("UpsertQuotaDomain on a closed db should fail")
	}
	if err := store.UpsertRuntimeProfile(ctx, RuntimeProfile{ID: "p", Runtime: RuntimeAPI}); err == nil {
		t.Error("UpsertRuntimeProfile on a closed db should fail")
	}
	if err := store.UpsertCredential(ctx, Credential{ID: "c", QuotaDomainRef: "d", RuntimeProfileRef: "p", Health: CredentialOK}); err == nil {
		t.Error("UpsertCredential on a closed db should fail")
	}
	if err := store.UpsertLane(ctx, Lane{ID: "l", RuntimeProfileRef: "p", QuotaDomainRef: "d", Health: LaneHealthAvailable}); err == nil {
		t.Error("UpsertLane on a closed db should fail")
	}
	if err := store.RetireLane(ctx, "l", newTestClock().Now()); err == nil {
		t.Error("RetireLane on a closed db should fail")
	}
	if _, err := store.GetAccount(ctx, "a"); err == nil {
		t.Error("GetAccount on a closed db should fail")
	}
	if _, err := store.ListAccounts(ctx); err == nil {
		t.Error("ListAccounts on a closed db should fail")
	}
	if _, err := store.snapshot(ctx); err == nil {
		t.Error("snapshot on a closed db should fail")
	}
	if err := store.withTx(ctx, func(*Store) error { return nil }); err == nil {
		t.Error("withTx.BeginTx on a closed db should fail")
	}
}

func TestWithTxRequiresNewStore(t *testing.T) {
	s := &Store{}
	if err := s.withTx(context.Background(), func(*Store) error { return nil }); err == nil {
		t.Fatal("withTx on a Store not built via NewStore should fail")
	}
}

func TestWithTxRollsBackOnError(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	seedAccount(t, s, "acct-1")
	sentinel := cascade.New(cascade.KindInternal, "boom")
	err := s.withTx(ctx, func(tx *Store) error {
		if e := tx.UpsertAccount(ctx, Account{ID: "acct-2", Billing: BillingInfo{Kind: BillingAPI}, Role: AccountRoleWorkforce}); e != nil {
			return e
		}
		return sentinel
	})
	if err != sentinel {
		t.Fatalf("withTx should propagate the fn error, got %v", err)
	}
	if _, err := s.GetAccount(ctx, "acct-2"); err == nil {
		t.Error("a rolled-back write should not be visible")
	}
}
