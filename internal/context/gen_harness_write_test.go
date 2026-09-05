package context

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// The write-side half of the cross-writer conformance suite. Every
// registered writer's output is materialized through the same write path
// and must earn the same guarantees: a hand edit is never destroyed
// silently, a repeat run changes nothing, and a hand-authored file is added
// to rather than replaced.

// firstFileFor renders w over the golden corpus and returns its first file,
// which is the GCI tier's.
func firstFileFor(t *testing.T, w HarnessGenerator) HarnessFile {
	t.Helper()
	files, err := w.Generate(mergeGoldenCorpus(t))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(files) == 0 {
		t.Fatalf("Generate produced no files over the golden corpus")
	}
	return files[0]
}

// assertKindConflict fails unless err carries the taxonomy's KindConflict.
func assertKindConflict(t *testing.T, err error) {
	t.Helper()
	var ce *cascade.Error
	if !errors.As(err, &ce) {
		t.Fatalf("error %v is not a taxonomy error", err)
	}
	if ce.Kind != cascade.KindConflict {
		t.Fatalf("error kind = %v, want %v", ce.Kind, cascade.KindConflict)
	}
}

// TestHarnessWritersRefuseAHandEditedFile requires every writer's output to
// stop at a block somebody has edited, under the default policy, with the
// edit still on disk afterwards.
func TestHarnessWritersRefuseAHandEditedFile(t *testing.T) {
	for _, w := range conformanceWriters(t) {
		t.Run(w.name, func(t *testing.T) {
			f := firstFileFor(t, w.gen)
			path := filepath.Join(t.TempDir(), filepath.FromSlash(f.Name))
			if _, err := WriteHarnessFile(path, f, RefuseIfEdited); err != nil {
				t.Fatalf("first write: %v", err)
			}
			edited := strings.Replace(string(f.Content), "cascade.search", "cascade.search AND MY OWN NOTE", 1)
			if edited == string(f.Content) {
				t.Fatal("the tamper did not change anything, so this test proves nothing")
			}
			if err := os.WriteFile(path, []byte(edited), 0o600); err != nil {
				t.Fatalf("planting the hand edit: %v", err)
			}
			_, err := WriteHarnessFile(path, f, RefuseIfEdited)
			if err == nil {
				t.Fatal("the write overwrote a hand-edited block")
			}
			assertKindConflict(t, err)
			after, rerr := os.ReadFile(path) //nolint:gosec // path is this test's temp file.
			if rerr != nil {
				t.Fatalf("reading back: %v", rerr)
			}
			if string(after) != edited {
				t.Fatal("the hand edit did not survive the refusal")
			}
		})
	}
}

// TestHarnessWritersBackUpAHandEditedFile pins the other half of the
// policy: an overwrite is allowed only when the previous file is preserved
// and the result says where.
func TestHarnessWritersBackUpAHandEditedFile(t *testing.T) {
	for _, w := range conformanceWriters(t) {
		t.Run(w.name, func(t *testing.T) {
			f := firstFileFor(t, w.gen)
			path := filepath.Join(t.TempDir(), filepath.FromSlash(f.Name))
			if _, err := WriteHarnessFile(path, f, RefuseIfEdited); err != nil {
				t.Fatalf("first write: %v", err)
			}
			edited := strings.Replace(string(f.Content), "cascade.search", "my own note", 1)
			if err := os.WriteFile(path, []byte(edited), 0o600); err != nil {
				t.Fatalf("planting the hand edit: %v", err)
			}
			res, err := WriteHarnessFile(path, f, BackupIfEdited)
			if err != nil {
				t.Fatalf("backup write: %v", err)
			}
			if res.Action != ActionBackedUp {
				t.Fatalf("action = %v, want ActionBackedUp", res.Action)
			}
			saved, rerr := os.ReadFile(res.BackupPath) //nolint:gosec // path comes from the result under test.
			if rerr != nil {
				t.Fatalf("reading the backup: %v", rerr)
			}
			if string(saved) != edited {
				t.Fatal("the backup does not hold the edit it was supposed to preserve")
			}
		})
	}
}

// TestHarnessWritersRewriteIdempotently requires ten consecutive writes to
// leave exactly one managed block and to stop touching the file after the
// first, for every writer.
func TestHarnessWritersRewriteIdempotently(t *testing.T) {
	const rewrites = 10
	for _, w := range conformanceWriters(t) {
		t.Run(w.name, func(t *testing.T) {
			f := firstFileFor(t, w.gen)
			path := filepath.Join(t.TempDir(), filepath.FromSlash(f.Name))
			for i := 0; i < rewrites; i++ {
				res, err := WriteHarnessFile(path, f, RefuseIfEdited)
				if err != nil {
					t.Fatalf("write %d: %v", i, err)
				}
				want := ActionUnchanged
				if i == 0 {
					want = ActionCreated
				}
				if res.Action != want {
					t.Fatalf("write %d: action = %v, want %v", i, res.Action, want)
				}
			}
			got, err := os.ReadFile(path) //nolint:gosec // path is this test's temp file.
			if err != nil {
				t.Fatalf("reading back: %v", err)
			}
			if n := strings.Count(string(got), markerOpenPrefix); n != 1 {
				t.Fatalf("the file holds %d managed blocks after %d writes, want 1", n, rewrites)
			}
			if string(got) != string(f.Content) {
				t.Fatal("the file on disk is not the bytes the generator produced")
			}
		})
	}
}

// TestHarnessWritersAppendToAHandAuthoredFile requires a pre-existing file
// with no managed block to be added to, never truncated: that file is
// somebody's own instructions, and cascade is a guest in it.
func TestHarnessWritersAppendToAHandAuthoredFile(t *testing.T) {
	const prose = "# my own instructions\n\nkeep this line\n"
	for _, w := range conformanceWriters(t) {
		t.Run(w.name, func(t *testing.T) {
			f := firstFileFor(t, w.gen)
			path := filepath.Join(t.TempDir(), filepath.FromSlash(f.Name))
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatalf("creating the tier directory: %v", err)
			}
			if err := os.WriteFile(path, []byte(prose), 0o600); err != nil {
				t.Fatalf("planting the hand-authored file: %v", err)
			}
			res, err := WriteHarnessFile(path, f, RefuseIfEdited)
			if err != nil {
				t.Fatalf("write: %v", err)
			}
			if res.Action != ActionAppended {
				t.Fatalf("action = %v, want ActionAppended", res.Action)
			}
			got, rerr := os.ReadFile(path) //nolint:gosec // path is this test's temp file.
			if rerr != nil {
				t.Fatalf("reading back: %v", rerr)
			}
			if !strings.HasPrefix(string(got), prose) {
				t.Fatal("the hand-authored prose was truncated")
			}
			if !strings.Contains(string(got), markerOpenPrefix) {
				t.Fatal("the managed block was not appended")
			}
		})
	}
}
