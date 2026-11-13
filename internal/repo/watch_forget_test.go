package repo

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestWatchJournalForgetRepository(t *testing.T) {
	e := openEnv(t, t.TempDir(), filepath.Join(t.TempDir(), "forget.db"), "", false)
	j, err := NewWatchJournal(e.db)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	seedForgetRows(t, j)
	before, _ := j.Paths(ctx, "b")
	intents, _ := j.intents(ctx, "b")
	if err := j.ForgetRepository(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	assertForgottenRows(t, j, before, intents)
	err = j.ForgetRepository(ctx, "")
	if !cascade.HasKind(err, cascade.KindInvalidInput) || err.Error() != "invalid-input: repo: forget requires a repository id" {
		t.Fatalf("empty id: %v", err)
	}
}

func seedForgetRows(t *testing.T, j *WatchJournal) {
	t.Helper()
	ctx := t.Context()
	for _, id := range []string{"a", "b"} {
		if err := j.AdvanceCursor(ctx, Cursor{RepositoryID: id, FSSeq: 5, Generation: 2}); err != nil {
			t.Fatal(err)
		}
		for _, rel := range []string{"first.txt", "second.txt"} {
			if err := j.complete(ctx, id, rel, &PathState{Rel: rel, ContentSHA256: "hash", ChunkIDs: []string{id + rel}}); err != nil {
				t.Fatal(err)
			}
			if err := j.beginIntent(ctx, id, intent{Rel: rel, ContentSHA256: "new", Generation: 3}); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func assertForgottenRows(t *testing.T, j *WatchJournal, before map[string]PathState, intents []intent) {
	t.Helper()
	ctx := t.Context()
	for _, id := range []string{"a", "b"} {
		cur, ok, err := j.Cursor(ctx, id)
		if err != nil || !ok || cur.Generation != 2 || cur.FSSeq != 5 {
			t.Fatalf("cursor %s: %+v %v %v", id, cur, ok, err)
		}
	}
	paths, err := j.Paths(ctx, "a")
	left, ierr := j.intents(ctx, "a")
	if err != nil || ierr != nil || len(paths) != 0 || len(left) != 0 {
		t.Fatalf("forgotten rows: %v %v %v %v", paths, left, err, ierr)
	}
	after, err := j.Paths(ctx, "b")
	remaining, ierr := j.intents(ctx, "b")
	if err != nil || ierr != nil || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(intents, remaining) {
		t.Fatalf("other repository changed: %v %v %v %v", after, remaining, err, ierr)
	}
}
