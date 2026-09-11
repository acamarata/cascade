package repo

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver for this package's tests

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/storage/migrate"
)

type fakeMigrateClock struct{ t time.Time }

func (c fakeMigrateClock) Now() time.Time { return c.t }

func newTestMigrateClock() migrate.Clock {
	return fakeMigrateClock{t: time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC)}
}

// openTestDB opens a real modernc-sqlite database file under t.TempDir()
// (Art.2), applies the context/scope schema (the foreign-key target this
// package's own table needs) and then this package's own schema.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "repo-test.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	clock := newTestMigrateClock()
	if err := scope.ApplyScopeSchema(ctx, db, migrate.SQLiteEmitter{}, clock, "", ""); err != nil {
		t.Fatalf("ApplyScopeSchema: %v", err)
	}
	if err := ApplyInventorySchema(ctx, db, migrate.SQLiteEmitter{}, clock, "", ""); err != nil {
		t.Fatalf("ApplyInventorySchema: %v", err)
	}
	return db
}

func TestApplyInventorySchemaIdempotent(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	if err := ApplyInventorySchema(ctx, db, migrate.SQLiteEmitter{}, newTestMigrateClock(), "", ""); err != nil {
		t.Fatalf("second ApplyInventorySchema: %v", err)
	}
}

func seedRepository(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	graph := scope.NewGraphStore(db)
	if err := graph.PutRepository(context.Background(), scope.RepositoryRecord{ID: id, Remote: "https://example.com/x.git", PathHash: "abc"}); err != nil {
		t.Fatal(err)
	}
}

func TestStoreUpsertGetRoundTrip(t *testing.T) {
	db := openTestDB(t)
	seedRepository(t, db, "repo-1")
	s := NewStore(db)
	ctx := context.Background()

	inv := Inventory{
		Repository: RepositoryRef{ID: "repo-1", Remote: "https://example.com/x.git", PathHash: "abc"},
		Languages:  []LanguageFacts{{Language: LanguageGo, Detected: true, Commands: Commands{Build: "go build ./..."}, Evidence: []string{"go.mod"}}},
		Layout:     LayoutFacts{DirCount: 2, FileCount: 5},
		CI:         CIFacts{GitHubActions: true},
		Harness:    HarnessFacts{ClaudeMD: true},
		ScannedAt:  1234,
	}
	if err := s.Upsert(ctx, inv); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	got, ok, err := s.Get(ctx, "repo-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("Get: ok = false, want true")
	}
	if got.Layout.FileCount != 5 || got.Languages[0].Language != LanguageGo {
		t.Errorf("got = %+v, want round-tripped inventory", got)
	}

	inv.Layout.FileCount = 9
	if err := s.Upsert(ctx, inv); err != nil {
		t.Fatalf("second Upsert: %v", err)
	}
	got2, _, err := s.Get(ctx, "repo-1")
	if err != nil {
		t.Fatalf("Get after update: %v", err)
	}
	if got2.Layout.FileCount != 9 {
		t.Errorf("FileCount = %d after update, want 9", got2.Layout.FileCount)
	}
}

func TestStoreGetNotFound(t *testing.T) {
	db := openTestDB(t)
	s := NewStore(db)
	_, ok, err := s.Get(context.Background(), "missing")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if ok {
		t.Error("ok = true for a missing row, want false")
	}
}

func TestStoreList(t *testing.T) {
	db := openTestDB(t)
	seedRepository(t, db, "repo-a")
	seedRepository(t, db, "repo-b")
	s := NewStore(db)
	ctx := context.Background()

	for _, id := range []string{"repo-b", "repo-a"} {
		if err := s.Upsert(ctx, Inventory{Repository: RepositoryRef{ID: id}}); err != nil {
			t.Fatal(err)
		}
	}
	list, err := s.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 || list[0].Repository.ID != "repo-a" || list[1].Repository.ID != "repo-b" {
		t.Fatalf("list = %+v, want [repo-a repo-b] in order", list)
	}
}

func TestStoreUpsertRequiresID(t *testing.T) {
	db := openTestDB(t)
	s := NewStore(db)
	if err := s.Upsert(context.Background(), Inventory{}); err == nil {
		t.Fatal("Upsert with empty repository id: want error, got nil")
	}
}

func TestApplyInventorySchemaNilArgsRefused(t *testing.T) {
	if err := ApplyInventorySchema(context.Background(), nil, migrate.SQLiteEmitter{}, newTestMigrateClock(), "", ""); err == nil {
		t.Fatal("ApplyInventorySchema with nil db: want error, got nil")
	}
	db := openTestDB(t)
	if err := ApplyInventorySchema(context.Background(), db, migrate.SQLiteEmitter{}, nil, "", ""); err == nil {
		t.Fatal("ApplyInventorySchema with nil clock: want error, got nil")
	}
}

func TestStoreGetEmptyIDRefused(t *testing.T) {
	db := openTestDB(t)
	s := NewStore(db)
	if _, _, err := s.Get(context.Background(), ""); err == nil {
		t.Fatal("Get with empty id: want error, got nil")
	}
}

func TestStoreGetDecodeError(t *testing.T) {
	db := openTestDB(t)
	seedRepository(t, db, "repo-bad")
	if _, err := db.Exec(`INSERT INTO context_repo_inventory (repository_id, inventory_json, scanned_at) VALUES (?, ?, ?)`,
		"repo-bad", "{not json", 0); err != nil {
		t.Fatal(err)
	}
	s := NewStore(db)
	if _, _, err := s.Get(context.Background(), "repo-bad"); err == nil {
		t.Fatal("Get with corrupted stored JSON: want error, got nil")
	}
}

func TestStoreListDecodeError(t *testing.T) {
	db := openTestDB(t)
	seedRepository(t, db, "repo-bad")
	if _, err := db.Exec(`INSERT INTO context_repo_inventory (repository_id, inventory_json, scanned_at) VALUES (?, ?, ?)`,
		"repo-bad", "{not json", 0); err != nil {
		t.Fatal(err)
	}
	s := NewStore(db)
	if _, err := s.List(context.Background()); err == nil {
		t.Fatal("List with corrupted stored JSON: want error, got nil")
	}
}

func TestStoreUpsertClosedDB(t *testing.T) {
	db := openTestDB(t)
	seedRepository(t, db, "repo-x")
	s := NewStore(db)
	_ = db.Close()
	if err := s.Upsert(context.Background(), Inventory{Repository: RepositoryRef{ID: "repo-x"}}); err == nil {
		t.Fatal("Upsert against a closed db: want error, got nil")
	}
	if _, _, err := s.Get(context.Background(), "repo-x"); err == nil {
		t.Fatal("Get against a closed db: want error, got nil")
	}
	if _, err := s.List(context.Background()); err == nil {
		t.Fatal("List against a closed db: want error, got nil")
	}
}
