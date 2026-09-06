package lifecycle

// Purpose: `cascade recall index update` (R-16.8) — a git-diff-driven
// incremental re-ingest of changed paths only, using the S-10.T1 stable
// chunk ids and the S-10.T3 content-hash dedupe so an unchanged file's
// chunks are never re-embedded.
//
// Inputs: the persisted R-21.189 generation marker (the diff's starting
// point) and an injected GitDiffFunc computing the changed paths and the
// tree hash the diff ended at.
//
// Outputs: an UpdateResult, or a pkg/cascade taxonomy error. Refuses with
// KindConflict when no marker is recorded yet — update has nothing to
// diff against, and `cascade recall index rebuild` is the documented
// first step (R-21.189's repair path).
//
// Constraints: only the files GitDiffFunc reports changed are re-chunked
// and re-embedded; every other file's manifest entry is left untouched.
// Deleted paths retract exactly their own manifest's chunk ids, never a
// whole corpus.
//
// SPORT: internal.retrieval.lifecycle.Manager/ADDED (P1-E06-W2-S11-T4).
import (
	"context"
	"sort"

	"github.com/acamarata/cascade/internal/retrieval"
	"github.com/acamarata/cascade/internal/retrieval/embed"
	"github.com/acamarata/cascade/pkg/cascade"
)

// ChangedPath is one path a GitDiffFunc reports as changed since a prior
// generation marker.
type ChangedPath struct {
	Path string
	// Deleted is true when the path no longer exists in the working set.
	// False covers both "added" and "modified" — update treats both
	// identically: re-chunk and re-write.
	Deleted bool
}

// GitDiffFunc reports the paths that changed between sinceTreeHash and
// the current working set, and the tree hash the diff ended at. The real
// implementation (the composition root's) shells out to git; tests inject
// a fixed diff.
type GitDiffFunc func(ctx context.Context, sinceTreeHash string) ([]ChangedPath, string, error)

// UpdateResult reports one update run's outcome.
type UpdateResult struct {
	FilesChanged  int
	ChunksWritten int
	ChunksDeleted int
	// Converged is true when the diff reported no changes.
	Converged bool
	Marker    string
}

// Update performs the R-16.8 incremental re-ingest. diff is required;
// passing nil is a caller error, not a degraded mode, because unlike the
// vector leg an update verb with no diff source can do nothing at all.
func (m *Manager) Update(ctx context.Context, diff GitDiffFunc) (UpdateResult, error) {
	if diff == nil {
		return UpdateResult{}, cascade.New(cascade.KindInvalidInput, "lifecycle: update: no git diff function")
	}
	marker, ok := readMarker(ctx, m.store)
	if !ok {
		return UpdateResult{}, cascade.New(cascade.KindConflict,
			"lifecycle: update: no generation marker recorded; run `cascade recall index rebuild` first")
	}
	changed, newTree, err := diff(ctx, marker.TreeHash)
	if err != nil {
		return UpdateResult{}, cascade.Wrap(cascade.KindUnavailable, err, "lifecycle: update: compute git diff")
	}
	if len(changed) == 0 {
		if err := writeMarker(ctx, m.store, newTree, m.clock.Now()); err != nil {
			return UpdateResult{}, err
		}
		return UpdateResult{Converged: true, Marker: newTree}, nil
	}
	return m.applyChanges(ctx, changed, newTree)
}

// applyChanges re-chunks and re-embeds every changed source file, and
// retracts every deleted path's previously recorded chunks.
func (m *Manager) applyChanges(ctx context.Context, changed []ChangedPath, newTree string) (UpdateResult, error) {
	touched := touchedPaths(changed)
	sources, err := m.sourcesOrEmpty(ctx)
	if err != nil {
		return UpdateResult{}, err
	}
	written, err := m.rewriteTouched(ctx, sources, touched)
	if err != nil {
		return UpdateResult{}, err
	}
	deleted, err := m.retractDeleted(ctx, deletedPaths(changed))
	if err != nil {
		return UpdateResult{}, err
	}
	if err := writeMarker(ctx, m.store, newTree, m.clock.Now()); err != nil {
		return UpdateResult{}, err
	}
	return UpdateResult{
		FilesChanged: len(changed), ChunksWritten: written, ChunksDeleted: deleted,
		Converged: written == 0 && deleted == 0, Marker: newTree,
	}, nil
}

// rewriteTouched chunks and writes only the files named in touched,
// across every registered source, and merges the result into each
// corpus's manifest (leaving untouched files' entries exactly as they
// were). It returns the number of chunks written.
func (m *Manager) rewriteTouched(ctx context.Context, sources []Source, touched map[string]bool) (int, error) {
	written := 0
	for _, src := range sources {
		var files []SourceFile
		for _, f := range src.Files {
			if touched[f.Path] {
				files = append(files, f)
			}
		}
		if len(files) == 0 {
			continue
		}
		chunks, err := chunkFiles(files)
		if err != nil {
			return written, err
		}
		if err := m.index.Write(ctx, src.Corpus.ID, chunks); err != nil {
			return written, err
		}
		if m.pipeline != nil && len(chunks) > 0 {
			if _, err := m.pipeline.Run(ctx, embed.Request{Corpus: src.Corpus, Chunks: chunks}); err != nil {
				return written, err
			}
		}
		if err := m.mergeManifest(ctx, src.Corpus.ID, files, chunks); err != nil {
			return written, err
		}
		written += len(chunks)
	}
	return written, nil
}

// mergeManifest replaces touched paths' entries in corpusID's manifest
// with entries built from chunks, leaving every other path's entry as it
// was.
func (m *Manager) mergeManifest(ctx context.Context, corpusID string, files []SourceFile, chunks []retrieval.Chunk) error {
	prev, _, err := readManifest(ctx, m.store, corpusID)
	if err != nil {
		return err
	}
	touchedPathSet := make(map[string]bool, len(files))
	for _, f := range files {
		touchedPathSet[f.Path] = true
	}
	var kept []manifestEntry
	for _, e := range prev {
		if !touchedPathSet[e.Path] {
			kept = append(kept, e)
		}
	}
	kept = append(kept, manifestFor(chunks)...)
	sort.Slice(kept, func(i, j int) bool { return kept[i].Path < kept[j].Path })
	return m.putManifest(ctx, corpusID, kept)
}

// retractDeleted removes every deleted path's previously recorded chunks
// from both legs and from its manifest, across every known corpus.
func (m *Manager) retractDeleted(ctx context.Context, deleted map[string]bool) (int, error) {
	if len(deleted) == 0 {
		return 0, nil
	}
	corpusIDs, err := m.knownCorpora(ctx)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, corpusID := range corpusIDs {
		removed, err := m.retractDeletedFromCorpus(ctx, corpusID, deleted)
		if err != nil {
			return count, err
		}
		count += removed
	}
	return count, nil
}

// retractDeletedFromCorpus is retractDeleted's single-corpus body, split
// out under the 50-line function cap.
func (m *Manager) retractDeletedFromCorpus(ctx context.Context, corpusID string, deleted map[string]bool) (int, error) {
	entries, ok, err := readManifest(ctx, m.store, corpusID)
	if err != nil || !ok {
		return 0, err
	}
	var kept []manifestEntry
	var ids []string
	for _, e := range entries {
		if deleted[e.Path] {
			ids = append(ids, e.ChunkIDs...)
			continue
		}
		kept = append(kept, e)
	}
	if len(ids) == 0 {
		return 0, nil
	}
	if err := m.retract(ctx, ids); err != nil {
		return 0, err
	}
	if err := m.putManifest(ctx, corpusID, kept); err != nil {
		return 0, err
	}
	return len(ids), nil
}

// touchedPaths returns the non-deleted paths from changed.
func touchedPaths(changed []ChangedPath) map[string]bool {
	out := make(map[string]bool, len(changed))
	for _, c := range changed {
		if !c.Deleted {
			out[c.Path] = true
		}
	}
	return out
}

// deletedPaths returns the deleted paths from changed.
func deletedPaths(changed []ChangedPath) map[string]bool {
	out := make(map[string]bool, len(changed))
	for _, c := range changed {
		if c.Deleted {
			out[c.Path] = true
		}
	}
	return out
}
