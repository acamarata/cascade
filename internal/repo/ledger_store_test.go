package repo

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/storage/migrate"
)

func openLedgerTestDB(t *testing.T) *LedgerStore {
	t.Helper()
	db := openTestDB(t)
	if err := ApplyLedgerSchema(context.Background(), db, migrate.SQLiteEmitter{}, newTestMigrateClock(), "", ""); err != nil {
		t.Fatalf("ApplyLedgerSchema: %v", err)
	}
	return NewLedgerStore(db)
}

func TestApplyLedgerSchemaIdempotent(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	clock := newTestMigrateClock()
	if err := ApplyLedgerSchema(ctx, db, migrate.SQLiteEmitter{}, clock, "", ""); err != nil {
		t.Fatalf("first ApplyLedgerSchema: %v", err)
	}
	if err := ApplyLedgerSchema(ctx, db, migrate.SQLiteEmitter{}, clock, "", ""); err != nil {
		t.Fatalf("second ApplyLedgerSchema: %v", err)
	}
}

func TestLedgerStoreAppendGetRoundTrip(t *testing.T) {
	ls := openLedgerTestDB(t)
	seedRepository(t, ls.db, "repo-1")
	ctx := context.Background()

	f := validFact()
	f.RepositoryID = "repo-1"
	if err := ls.Append(ctx, f); err != nil {
		t.Fatalf("Append: %v", err)
	}

	got, ok, err := ls.Get(ctx, f.ID, f.Version)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("Get: ok = false, want true")
	}
	if got.Fact != f.Fact || got.State != FactProposed {
		t.Fatalf("got = %+v, want round-tripped fact", got)
	}
}

func TestLedgerStoreAppendRejectsDuplicateVersion(t *testing.T) {
	ls := openLedgerTestDB(t)
	seedRepository(t, ls.db, "repo-1")
	ctx := context.Background()
	f := validFact()
	f.RepositoryID = "repo-1"
	if err := ls.Append(ctx, f); err != nil {
		t.Fatalf("first Append: %v", err)
	}
	if err := ls.Append(ctx, f); err == nil {
		t.Fatal("second Append of the same (id, version): err = nil, want typed conflict error")
	}
}

func TestLedgerStoreAppendRejectsInvalidFact(t *testing.T) {
	ls := openLedgerTestDB(t)
	bad := validFact()
	bad.Fact = ""
	if err := ls.Append(context.Background(), bad); err == nil {
		t.Fatal("Append with an invalid fact: err = nil, want typed error")
	}
}

func TestLedgerStoreGetNotFound(t *testing.T) {
	ls := openLedgerTestDB(t)
	_, ok, err := ls.Get(context.Background(), "missing", 1)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if ok {
		t.Fatal("Get: ok = true, want false for a missing row")
	}
}

func TestLedgerStoreGetRejectsEmptyFactID(t *testing.T) {
	ls := openLedgerTestDB(t)
	if _, _, err := ls.Get(context.Background(), "", 1); err == nil {
		t.Fatal("Get with empty fact id: err = nil, want typed error")
	}
}

func TestLedgerStoreListFiltersBySubjectAndState(t *testing.T) {
	ls := openLedgerTestDB(t)
	seedRepository(t, ls.db, "repo-1")
	ctx := context.Background()

	f1 := validFact()
	f1.ID = "f1"
	f1.RepositoryID = "repo-1"
	f1.Subject = SubjectLanguages
	if err := ls.Append(ctx, f1); err != nil {
		t.Fatal(err)
	}

	f2 := validFact()
	f2.ID = "f2"
	f2.RepositoryID = "repo-1"
	f2.Subject = SubjectBuildCmd
	f2.Fact = "go build ./..."
	if err := ls.Append(ctx, f2); err != nil {
		t.Fatal(err)
	}

	all, err := ls.List(ctx, LedgerFilter{RepositoryID: "repo-1"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("List(no filter) len = %d, want 2", len(all))
	}

	onlyLangs, err := ls.List(ctx, LedgerFilter{RepositoryID: "repo-1", Subject: SubjectLanguages})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(onlyLangs) != 1 || onlyLangs[0].ID != "f1" {
		t.Fatalf("List(Subject=languages) = %+v, want [f1]", onlyLangs)
	}

	onlyProposed, err := ls.List(ctx, LedgerFilter{RepositoryID: "repo-1", State: FactProposed})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(onlyProposed) != 2 {
		t.Fatalf("List(State=proposed) len = %d, want 2", len(onlyProposed))
	}
}

func TestLedgerStoreListRejectsEmptyRepositoryID(t *testing.T) {
	ls := openLedgerTestDB(t)
	if _, err := ls.List(context.Background(), LedgerFilter{}); err == nil {
		t.Fatal("List with empty repository id: err = nil, want typed error")
	}
}

func TestLedgerStoreUpdateState(t *testing.T) {
	ls := openLedgerTestDB(t)
	seedRepository(t, ls.db, "repo-1")
	ctx := context.Background()
	f := validFact()
	f.RepositoryID = "repo-1"
	if err := ls.Append(ctx, f); err != nil {
		t.Fatal(err)
	}

	accepted, err := Accept(f, "detector:languages")
	if err != nil {
		t.Fatal(err)
	}
	if err := ls.UpdateState(ctx, accepted); err != nil {
		t.Fatalf("UpdateState: %v", err)
	}

	got, ok, err := ls.Get(ctx, f.ID, f.Version)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || got.State != FactAccepted || got.AcceptedBy != "detector:languages" {
		t.Fatalf("got = %+v, want the accepted state persisted", got)
	}
}

func TestLedgerStoreUpdateStateNotFound(t *testing.T) {
	ls := openLedgerTestDB(t)
	seedRepository(t, ls.db, "repo-1")
	f := validFact()
	f.RepositoryID = "repo-1"
	f.State = FactAccepted
	f.AcceptedBy = "someone"
	if err := ls.UpdateState(context.Background(), f); err == nil {
		t.Fatal("UpdateState on a never-appended fact: err = nil, want typed not-found error")
	}
}
