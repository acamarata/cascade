package lifecycle_test

// Purpose: TestRecallIndex-prefixed tests for Update (R-16.8) — the
// git-diff-driven incremental re-ingest, the counter-asserted "only the
// changed file's chunks are re-embedded" proof, the marker round-trip,
// and the fail-closed absent-marker refusal.
//
// SPORT: internal.retrieval.lifecycle.Manager/ADDED (P1-E06-W2-S11-T4).

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/retrieval/lifecycle"
	"github.com/acamarata/cascade/pkg/cascade"
)

// twoFileSource returns a Source with two files under testCorpus.
func twoFileSource(pathA, bodyA, pathB, bodyB string) []lifecycle.Source {
	return []lifecycle.Source{{Corpus: testCorpus, Files: []lifecycle.SourceFile{
		{Path: pathA, Content: []byte(bodyA)}, {Path: pathB, Content: []byte(bodyB)},
	}}}
}

// diffFunc returns a fixed lifecycle.GitDiffFunc for one test.
func diffFunc(changed []lifecycle.ChangedPath, newTree string) lifecycle.GitDiffFunc {
	return func(context.Context, string) ([]lifecycle.ChangedPath, string, error) { return changed, newTree, nil }
}

// TestRecallIndexUpdate is the ticket's contract-named smoke test for the
// whole verb: rebuild seeds a marker, an edited file's update advances
// the marker and re-indexes only that file. The detailed proofs (the
// counter-assertion, convergence, deletion, and the fail-closed refusal)
// live in the TestRecallIndexUpdate*-prefixed tests below; this is the
// literal name the ticket's own checks list runs.
func TestRecallIndexUpdate(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	m := h.manager(twoFileSource("a.md", "# A\n\none", "b.md", "# B\n\ntwo"))
	if _, err := m.Rebuild(ctx); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	res, err := m.Update(ctx, diffFunc([]lifecycle.ChangedPath{{Path: "a.md"}}, "tree-2"))
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if res.Marker != "tree-2" || res.FilesChanged != 1 {
		t.Fatalf("want the marker advanced and exactly one file changed, got %+v", res)
	}
}

// TestRecallIndexUpdateRequiresPriorMarker proves update refuses
// (KindConflict) with no generation marker recorded — the fail-closed
// "run rebuild first" case.
func TestRecallIndexUpdateRequiresPriorMarker(t *testing.T) {
	h := newHarness(t)
	m := h.manager(twoFileSource("a.md", "# A\n\none", "b.md", "# B\n\ntwo"))
	_, err := m.Update(context.Background(), diffFunc(nil, "tree-2"))
	if !cascade.HasKind(err, cascade.KindConflict) {
		t.Fatalf("want KindConflict with no prior marker, got %v", err)
	}
}

// TestRecallIndexUpdateRequiresDiffFunc proves update refuses with a nil
// diff function rather than panicking.
func TestRecallIndexUpdateRequiresDiffFunc(t *testing.T) {
	h := newHarness(t)
	m := h.manager(nil)
	if _, err := m.Update(context.Background(), nil); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("want KindInvalidInput with a nil diff func, got %v", err)
	}
}

// TestRecallIndexUpdateOnlyReEmbedsChangedFile is the counter-asserted
// proof: after a rebuild indexes two files, editing only one and running
// update embeds ONLY that file's new chunk, leaving the other file's
// chunk id (and its embedding) untouched.
func TestRecallIndexUpdateOnlyReEmbedsChangedFile(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	m := h.manager(twoFileSource("a.md", "# A\n\noriginal a content", "b.md", "# B\n\noriginal b content"))
	if _, err := m.Rebuild(ctx); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	callsAfterRebuild := h.embedder.calls

	// Edit only a.md; b.md is untouched.
	editedSource := twoFileSource("a.md", "# A\n\nEDITED a content", "b.md", "# B\n\noriginal b content")
	m2 := h.manager(editedSource)
	res, err := m2.Update(ctx, diffFunc([]lifecycle.ChangedPath{{Path: "a.md"}}, "tree-2"))
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if res.FilesChanged != 1 || res.ChunksWritten == 0 {
		t.Fatalf("want exactly one file's chunks written, got %+v", res)
	}
	newCalls := h.embedder.calls - callsAfterRebuild
	if newCalls != res.ChunksWritten {
		t.Fatalf("counter-assertion failed: want exactly %d new embed calls (a.md's chunks only), got %d",
			res.ChunksWritten, newCalls)
	}
	if res.Marker != "tree-2" {
		t.Fatalf("want the marker advanced to the diff's ending tree, got %q", res.Marker)
	}
}

// TestRecallIndexUpdateConvergesOnNoChanges proves an update whose diff
// reports nothing changed is a real, convergent no-op that still advances
// the marker.
func TestRecallIndexUpdateConvergesOnNoChanges(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	m := h.manager(oneSource("a.md", "# A\n\nbody"))
	if _, err := m.Rebuild(ctx); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	res, err := m.Update(ctx, diffFunc(nil, "tree-2"))
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !res.Converged || res.Marker != "tree-2" {
		t.Fatalf("want a convergent update advancing the marker, got %+v", res)
	}
}

// TestRecallIndexUpdateRetractsDeletedPath proves a deleted path's
// previously indexed chunks are retracted.
func TestRecallIndexUpdateRetractsDeletedPath(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	m := h.manager(twoFileSource("a.md", "# A\n\none", "b.md", "# B\n\ntwo"))
	if _, err := m.Rebuild(ctx); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	res, err := m.Update(ctx, diffFunc([]lifecycle.ChangedPath{{Path: "b.md", Deleted: true}}, "tree-2"))
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if res.ChunksDeleted == 0 {
		t.Fatalf("want b.md's chunks retracted, got %+v", res)
	}
}
