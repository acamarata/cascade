package repo

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/storage/migrate"
)

// openGraphTestDB reuses store_test.go's openTestDB (context/scope +
// inventory schema) and additionally applies GraphMigrationSet.
func openGraphTestDB(t *testing.T) *GraphStore {
	t.Helper()
	db := openTestDB(t)
	if err := ApplyGraphSchema(context.Background(), db, migrate.SQLiteEmitter{}, newTestMigrateClock(), "", ""); err != nil {
		t.Fatalf("ApplyGraphSchema: %v", err)
	}
	return NewGraphStore(db)
}

func TestApplyGraphSchemaIdempotent(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	clock := newTestMigrateClock()
	if err := ApplyGraphSchema(ctx, db, migrate.SQLiteEmitter{}, clock, "", ""); err != nil {
		t.Fatalf("first ApplyGraphSchema: %v", err)
	}
	if err := ApplyGraphSchema(ctx, db, migrate.SQLiteEmitter{}, clock, "", ""); err != nil {
		t.Fatalf("second ApplyGraphSchema: %v", err)
	}
}

func TestGraphStoreUpsertGetRoundTrip(t *testing.T) {
	gs := openGraphTestDB(t)
	db := gs.db
	seedRepository(t, db, "repo-1")
	ctx := context.Background()

	g := SymbolGraph{
		Nodes: []GraphNode{{ID: "pkg", Kind: NodePackage, Package: "pkg"}},
		Edges: nil,
	}
	if err := gs.Upsert(ctx, "repo-1", 1, g, 1000); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	got, gen, ok, err := gs.Get(ctx, "repo-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("Get: ok = false, want true")
	}
	if gen != 1 || len(got.Nodes) != 1 {
		t.Fatalf("got = %+v gen=%d, want 1 node at generation 1", got, gen)
	}
}

// TestGraphStoreUpsertReplacesAtomically proves a rescan's Upsert fully
// replaces the prior graph -- a reader never observes a mix of the old
// and new graphs.
func TestGraphStoreUpsertReplacesAtomically(t *testing.T) {
	gs := openGraphTestDB(t)
	seedRepository(t, gs.db, "repo-1")
	ctx := context.Background()

	g1 := SymbolGraph{Nodes: []GraphNode{{ID: "old", Kind: NodePackage, Package: "old"}}}
	if err := gs.Upsert(ctx, "repo-1", 1, g1, 1000); err != nil {
		t.Fatalf("first Upsert: %v", err)
	}
	g2 := SymbolGraph{Nodes: []GraphNode{{ID: "new", Kind: NodePackage, Package: "new"}}}
	if err := gs.Upsert(ctx, "repo-1", 2, g2, 2000); err != nil {
		t.Fatalf("second Upsert: %v", err)
	}

	got, gen, ok, err := gs.Get(ctx, "repo-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok || gen != 2 || len(got.Nodes) != 1 || got.Nodes[0].ID != "new" {
		t.Fatalf("got = %+v gen=%d, want only the second generation's graph", got, gen)
	}
}

func TestGraphStoreGetNotFound(t *testing.T) {
	gs := openGraphTestDB(t)
	_, _, ok, err := gs.Get(context.Background(), "missing")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if ok {
		t.Fatal("Get: ok = true, want false for a missing row")
	}
}

func TestGraphStoreUpsertRejectsEmptyRepositoryID(t *testing.T) {
	gs := openGraphTestDB(t)
	err := gs.Upsert(context.Background(), "", 1, SymbolGraph{}, 1000)
	if err == nil {
		t.Fatal("Upsert with empty repository id: err = nil, want typed error")
	}
}

func TestGraphStoreUpsertRejectsInvalidGraph(t *testing.T) {
	gs := openGraphTestDB(t)
	seedRepository(t, gs.db, "repo-1")
	bad := SymbolGraph{Edges: []GraphEdge{{From: "a", To: "b", Kind: EdgeImports}}}
	err := gs.Upsert(context.Background(), "repo-1", 1, bad, 1000)
	if err == nil {
		t.Fatal("Upsert with a dangling edge: err = nil, want typed error")
	}
}

func TestGraphStoreGetRejectsEmptyRepositoryID(t *testing.T) {
	gs := openGraphTestDB(t)
	_, _, _, err := gs.Get(context.Background(), "")
	if err == nil {
		t.Fatal("Get with empty repository id: err = nil, want typed error")
	}
}

func TestGraphStoreList(t *testing.T) {
	gs := openGraphTestDB(t)
	seedRepository(t, gs.db, "repo-a")
	seedRepository(t, gs.db, "repo-b")
	ctx := context.Background()
	g := SymbolGraph{Nodes: []GraphNode{{ID: "x", Kind: NodePackage, Package: "x"}}}
	if err := gs.Upsert(ctx, "repo-b", 1, g, 1000); err != nil {
		t.Fatal(err)
	}
	if err := gs.Upsert(ctx, "repo-a", 1, g, 1000); err != nil {
		t.Fatal(err)
	}
	ids, err := gs.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(ids) != 2 || ids[0] != "repo-a" || ids[1] != "repo-b" {
		t.Fatalf("List = %v, want [repo-a repo-b]", ids)
	}
}
