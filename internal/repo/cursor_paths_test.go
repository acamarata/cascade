package repo

// Purpose: error and edge paths of the watch journal: schema guards, a
//   closed database on every read and write, a corrupt chunk-id row.
// SPORT: repo/maintenance-journal/ADD test coverage (P1-GEN-04).

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

// newJournalDB opens a fresh sqlite file carrying only the journal schema.
func newJournalDB(t *testing.T) (*sql.DB, *WatchJournal) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "journal.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if err := ApplyWatchJournalSchema(context.Background(), db, migrate.SQLiteEmitter{}, newTestMigrateClock(), "", ""); err != nil {
		t.Fatal(err)
	}
	j, err := NewWatchJournal(db)
	if err != nil {
		t.Fatal(err)
	}
	return db, j
}

// expectKind fails unless err is a *cascade.Error of kind k.
func expectKind(t *testing.T, what string, err error, k cascade.Kind) {
	t.Helper()
	var ce *cascade.Error
	if !errors.As(err, &ce) || ce.Kind != k {
		t.Errorf("%s: err = %v, want %s", what, err, k)
	}
}

func TestApplyWatchJournalSchemaGuards(t *testing.T) {
	ctx := context.Background()
	expectKind(t, "nil db", ApplyWatchJournalSchema(ctx, nil, migrate.SQLiteEmitter{}, newTestMigrateClock(), "", ""), cascade.KindInvalidInput)
	db, _ := newJournalDB(t)
	expectKind(t, "nil clock", ApplyWatchJournalSchema(ctx, db, migrate.SQLiteEmitter{}, nil, "", ""), cascade.KindInvalidInput)
}

func TestWatchJournalClosedDBIsUnavailable(t *testing.T) {
	ctx := context.Background()
	db, j := newJournalDB(t)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	_, _, err := j.Cursor(ctx, testRepoID)
	expectKind(t, "Cursor", err, cascade.KindUnavailable)
	expectKind(t, "AdvanceCursor", j.AdvanceCursor(ctx, Cursor{RepositoryID: testRepoID, Generation: 1}), cascade.KindUnavailable)
	_, err = j.Paths(ctx, testRepoID)
	expectKind(t, "Paths", err, cascade.KindUnavailable)
	_, err = j.intents(ctx, testRepoID)
	expectKind(t, "intents", err, cascade.KindUnavailable)
	expectKind(t, "beginIntent", j.beginIntent(ctx, testRepoID, intent{Rel: "a.go"}), cascade.KindUnavailable)
	expectKind(t, "markLegs", j.markLegs(ctx, testRepoID, "a.go", 1), cascade.KindUnavailable)
	expectKind(t, "complete", j.complete(ctx, testRepoID, "a.go", nil), cascade.KindUnavailable)
}

func TestAdvanceCursorRequiresRepositoryID(t *testing.T) {
	_, j := newJournalDB(t)
	expectKind(t, "empty id", j.AdvanceCursor(context.Background(), Cursor{Generation: 1}), cascade.KindInvalidInput)
	if _, ok := mustCursor(t, j); ok {
		t.Fatal("a refused cursor write left a row")
	}
}

func TestWatchJournalCorruptChunkIDsIsIntegrity(t *testing.T) {
	db, j := newJournalDB(t)
	if _, err := db.Exec(`INSERT INTO `+tableWatchPath+` (repository_id, rel, content_sha256, chunk_ids_json, vector_pending)
		VALUES (?, 'a.go', 'h', '{not json', 0)`, testRepoID); err != nil {
		t.Fatal(err)
	}
	_, err := j.Paths(context.Background(), testRepoID)
	expectKind(t, "Paths", err, cascade.KindIntegrity)
}

func TestWatchJournalCompleteWritesAndClears(t *testing.T) {
	ctx := context.Background()
	_, j := newJournalDB(t)
	if err := j.beginIntent(ctx, testRepoID, intent{Rel: "a.go", ContentSHA256: "h1", Generation: 1, Deleted: true}); err != nil {
		t.Fatal(err)
	}
	st := &PathState{Rel: "a.go", ContentSHA256: "h1", VectorPending: true}
	if err := j.complete(ctx, testRepoID, "a.go", st); err != nil {
		t.Fatal(err)
	}
	got := mustPaths(t, j)["a.go"]
	if got.ContentSHA256 != "h1" || !got.VectorPending || got.ChunkIDs == nil || len(got.ChunkIDs) != 0 {
		t.Fatalf("path row = %+v, want h1, pending, an empty (non-null) chunk list", got)
	}
	if len(mustIntents(t, j)) != 0 {
		t.Fatal("complete left the intent")
	}
	if err := j.complete(ctx, testRepoID, "a.go", nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := mustPaths(t, j)["a.go"]; ok {
		t.Fatal("complete(nil) kept the path row")
	}
}
