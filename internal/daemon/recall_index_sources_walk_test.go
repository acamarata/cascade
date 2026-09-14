package daemon

// Purpose: mutation-provable coverage for recall_index_sources.go's real
//   edge behaviours beyond the one-happy-path fixture in
//   recall_index_sources_test.go: a retrieval.sources[] root that does not
//   exist yet, a root holding only unchunkable content, the .git skip, a
//   chunkable-but-unreadable file, and retrievalSourceCorpusID's per-index
//   naming. Split into its own file to respect the 300-line cap.
// Constraints: no doubles - every case is a real directory (or symlink)
//   under t.TempDir(), walked by the real walkChunkableSourceFiles/Sources.

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestWalkChunkableSourceFiles_MissingRootIsEmptyNotError pins the
// documented contract (recall_index_sources.go's own package doc: "a
// source root that does not exist yet is a real, convergent empty source,
// not an error"): a retrieval.sources[] entry naming a directory that
// hasn't been created yet must not fail recall.index.rebuild, it must
// just contribute nothing.
func TestWalkChunkableSourceFiles_MissingRootIsEmptyNotError(t *testing.T) {
	root := filepath.Join(t.TempDir(), "does-not-exist-yet")
	files, err := walkChunkableSourceFiles(root)
	if err != nil {
		t.Fatalf("walkChunkableSourceFiles(%s): want nil error for a missing root, got %v", root, err)
	}
	if files != nil {
		t.Fatalf("walkChunkableSourceFiles(%s) = %+v, want nil (a missing root is a convergent empty source)", root, files)
	}
}

// TestWalkChunkableSourceFiles_SkipsUnchunkableAndGitDirs proves the other
// two documented skip behaviours in one real walk: a file whose extension
// retrieval.ChunkerFor does not recognize contributes nothing (not an
// error), and a ".git" directory is never even descended into - so a
// chunkable .md file living inside .git is NOT returned, even though its
// extension alone would qualify it.
//
// MUTATION PROOF (recorded in full in the journal): removing the
// `if d.Name() == ".git" { return filepath.SkipDir }` special case in
// walkChunkableSourceFiles turns this red with the real message
// "walkChunkableSourceFiles(...) = [{Path:.../.git/HEAD.md ...}], want
// zero files" - descending into .git is the real regression this pins.
// Restoring the special case returns it to GREEN.
func TestWalkChunkableSourceFiles_SkipsUnchunkableAndGitDirs(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "image.png"), []byte("not text"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitDir := filepath.Join(root, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD.md"), []byte("# would be chunkable if reached"), 0o644); err != nil {
		t.Fatal(err)
	}

	files, err := walkChunkableSourceFiles(root)
	if err != nil {
		t.Fatalf("walkChunkableSourceFiles(%s): %v", root, err)
	}
	if len(files) != 0 {
		t.Fatalf("walkChunkableSourceFiles(%s) = %+v, want zero files (image.png is unchunkable, .git is skipped even though it holds a .md file)", root, files)
	}
}

// TestWalkChunkableSourceFiles_UnreadableChunkableFileErrors proves a file
// whose extension IS recognized but cannot be read is a real, loud error -
// unlike the unrecognized-extension case above, a read failure on content
// this code claims it will index must not be silently dropped. A broken
// symlink (extension .md, target missing) reproduces an unreadable file
// deterministically, independent of the test process's file permissions.
func TestWalkChunkableSourceFiles_UnreadableChunkableFileErrors(t *testing.T) {
	root := t.TempDir()
	broken := filepath.Join(root, "broken.md")
	if err := os.Symlink(filepath.Join(root, "does-not-exist.md"), broken); err != nil {
		t.Fatal(err)
	}

	files, err := walkChunkableSourceFiles(root)
	if err == nil {
		t.Fatalf("walkChunkableSourceFiles(%s) = %+v, nil error; want an error for a chunkable-but-unreadable file (broken symlink)", root, files)
	}
	kind, ok := cascade.KindOf(err)
	if !ok || kind != cascade.KindUnavailable {
		t.Fatalf("walkChunkableSourceFiles error kind = %v (ok=%v), want KindUnavailable", kind, ok)
	}
}

// TestRetrievalSourceCorpusID pins the exact per-index naming
// retrievalSourceCorpusID documents: index 0 keeps the bare CorpusIDCode
// (the common single-source case), later indices suffix with ":<i>" so
// two configured sources never collide in lifecycle's per-corpus manifest
// bookkeeping.
func TestRetrievalSourceCorpusID(t *testing.T) {
	cases := []struct {
		i    int
		want string
	}{
		{0, corpus.CorpusIDCode},
		{1, corpus.CorpusIDCode + ":1"},
		{2, corpus.CorpusIDCode + ":2"},
	}
	for _, tc := range cases {
		if got := retrievalSourceCorpusID(tc.i); got != tc.want {
			t.Errorf("retrievalSourceCorpusID(%d) = %q, want %q", tc.i, got, tc.want)
		}
	}
}

// TestConfigSourceProvider_Sources_MultipleRootsGetDistinctCorpusIDs
// drives configSourceProvider.Sources itself (not just the corpus-id
// helper in isolation) with two configured retrieval.sources[] roots,
// proving the real composition: each root's files land under its own
// distinctly-ID'd corpus, store-state asserted on the actual returned
// []lifecycle.Source, never on a call having merely happened.
func TestConfigSourceProvider_Sources_MultipleRootsGetDistinctCorpusIDs(t *testing.T) {
	root := t.TempDir()
	fp := fakePaths{root: root}

	dir0 := filepath.Join(root, "one")
	dir1 := filepath.Join(root, "two")
	if err := os.MkdirAll(dir0, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir1, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir0, "a.md"), []byte("# alpha content here"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir1, "b.go"), []byte("package beta\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	body := "[retrieval]\nsources = [" + strconv.Quote(dir0) + ", " + strconv.Quote(dir1) + "]\n"
	if err := os.WriteFile(fp.ConfigPath(), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	provider := configSourceProvider{configPath: fp.ConfigPath()}
	sources, err := provider.Sources(context.Background())
	if err != nil {
		t.Fatalf("Sources: %v", err)
	}
	if len(sources) != 2 {
		t.Fatalf("Sources() returned %d entries, want 2 (one per configured root)", len(sources))
	}
	if sources[0].Corpus.ID != corpus.CorpusIDCode {
		t.Errorf("sources[0].Corpus.ID = %q, want %q", sources[0].Corpus.ID, corpus.CorpusIDCode)
	}
	if sources[1].Corpus.ID != corpus.CorpusIDCode+":1" {
		t.Errorf("sources[1].Corpus.ID = %q, want %q", sources[1].Corpus.ID, corpus.CorpusIDCode+":1")
	}
	if len(sources[0].Files) != 1 || sources[0].Files[0].Path != filepath.Join(dir0, "a.md") {
		t.Errorf("sources[0].Files = %+v, want the one file in dir0 (%s)", sources[0].Files, dir0)
	}
	if len(sources[1].Files) != 1 || sources[1].Files[0].Path != filepath.Join(dir1, "b.go") {
		t.Errorf("sources[1].Files = %+v, want the one file in dir1 (%s)", sources[1].Files, dir1)
	}
}

// TestConfigSourceProvider_Sources_UnreadableFilePropagatesError proves the
// end-to-end wiring, not just the inner helper in isolation: a configured
// retrieval.sources[] root containing one chunkable-but-unreadable file
// fails the WHOLE Sources() call, matching the doc comment's contract that
// a read failure is a real error, never a silently-dropped source.
func TestConfigSourceProvider_Sources_UnreadableFilePropagatesError(t *testing.T) {
	root := t.TempDir()
	fp := fakePaths{root: root}

	sourceDir := filepath.Join(root, "src")
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	broken := filepath.Join(sourceDir, "broken.md")
	if err := os.Symlink(filepath.Join(sourceDir, "does-not-exist.md"), broken); err != nil {
		t.Fatal(err)
	}

	body := "[retrieval]\nsources = [" + strconv.Quote(sourceDir) + "]\n"
	if err := os.WriteFile(fp.ConfigPath(), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	provider := configSourceProvider{configPath: fp.ConfigPath()}
	sources, err := provider.Sources(context.Background())
	if err == nil {
		t.Fatalf("Sources() = %+v, nil error; want the whole call to fail on one unreadable configured source", sources)
	}
	kind, ok := cascade.KindOf(err)
	if !ok || kind != cascade.KindUnavailable {
		t.Fatalf("Sources() error kind = %v (ok=%v), want KindUnavailable", kind, ok)
	}
}
