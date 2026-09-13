// Package v1 uses this file to rebuild the v2 recall index over migrated
// content and report the rebuilt index's health.
//
// Purpose: after the v1 importers (memory.go, config.go, accounts.go,
// vault.go) have written their migrated content to disk, drive the real
// F/S-10 chunkers and the real F/S-11 index lifecycle over exactly that
// content, then verify the result.
// Inputs: the real lifecycle.Manager collaborators (RebuildDeps) plus the
// migrated corpus classification and the directories its files live under
// (RebuildOptions).
// Outputs: RebuildIndexResult (the real RebuildResult and VerifyReport), or
// a pkg/cascade taxonomy error.
// Constraints: no new ingest or chunking logic — chunking is
// retrieval.ChunkerFor (F/S-10) and the write/verify path is
// lifecycle.Manager (F/S-11.T4), called verbatim. This file supplies only
// the directory-walk glue lifecycle.SourceProvider requires and which no
// production caller has filled in yet (rebuild.go's own CONTRACT NOTE);
// see this ticket's journal for the contract-vs-tree note in full.
//
// CONTRACT NOTE (both sides quoted in full in this ticket's journal). The
// contract's own prose calls this "a single RebuildIndex(ctx, stores,
// opts) function." The tree has no "stores" vocabulary: the real
// lifecycle.Manager (the only F/S-11 entry point that exists) takes a
// ManagerOptions struct naming Store/Index/Vectors/Pipeline individually,
// and internal/daemon/recall_index.go's own production wiring — the
// composition-root precedent this ticket follows — builds one with no
// Pipeline/Vectors at all ("no embedder is configured at this composition
// root yet"). RebuildDeps below names the same collaborators under the
// same convention; opts carries the migrated corpus's classification and
// source roots, the one piece the daemon's own recall.index.rebuild RPC
// does not need because it has no migration to source from.
//
// SPORT: migration/v1/indexrebuild/ADD (P1-E26-W10-S53-T2).
package v1

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/acamarata/cascade/internal/retrieval"
	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/internal/retrieval/embed"
	"github.com/acamarata/cascade/internal/retrieval/lifecycle"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// RebuildDeps bundles the real F/S-11 collaborators RebuildIndex composes,
// named after lifecycle.ManagerOptions (the only F/S-11 entry point) so a
// caller reads this struct as "the same options recall_index.go's
// production wiring builds, plus the migration's own source roots."
type RebuildDeps struct {
	// CatalogPath is where the rebuilt catalog document is written; the
	// query-time recall.FileCatalog reads it back from here.
	CatalogPath string
	// Store is the FTS5 leg's backing store and this package's own
	// manifest/marker bookkeeping. Required.
	Store provider.Store
	// Index is the FTS5 leg's write side. Required.
	Index retrieval.Indexer
	// Vectors is the vector leg. Nil means no embedder is configured, the
	// same degradation recall_index.go's production wiring documents.
	Vectors provider.VectorStore
	// Pipeline embeds and upserts chunks. Nil alongside Vectors.
	Pipeline *embed.Pipeline
	// TreeHash computes the current generation marker. Required.
	TreeHash lifecycle.GitTreeHashFunc
	// Clock timestamps bookkeeping writes. Required.
	Clock runtime.Clock
}

// RebuildOptions configures one RebuildIndex call: the classification the
// migrated content is registered under, and the directories its files
// live in on disk.
type RebuildOptions struct {
	// Corpus classifies every file found under Roots. Required and must
	// validate (corpus.Corpus.Validate).
	Corpus corpus.Corpus
	// Roots are the directories RebuildIndex walks for migrated content.
	// A root that does not exist yet is a real, convergent empty source,
	// not an error (a migration that has not run memory import yet).
	// Every file recognized by retrieval.ChunkerFor is included; every
	// other file is skipped, not refused, because a migrated tree mixes
	// chunkable content with the importers' own non-corpus artifacts
	// (accounts, vault, config) that this ticket does not index.
	Roots []string
}

// RebuildIndexResult reports one RebuildIndex call's outcome.
type RebuildIndexResult struct {
	// Rebuild is the real lifecycle.Manager.Rebuild result.
	Rebuild lifecycle.RebuildResult
	// Verify is the real lifecycle.Manager.Verify result, taken
	// immediately after Rebuild so a caller sees the index's health in
	// the same call that built it (the "doctor --storage reports index
	// healthy after rebuild" acceptance criterion).
	Verify lifecycle.VerifyReport
}

// RebuildIndex re-runs the F/S-10 chunkers and the F/S-11 index lifecycle
// over the migrated content named by opts.Roots, then verifies the
// result. It is idempotent: lifecycle.Manager.Rebuild is convergent over
// content-addressed chunk ids (rebuild.go), so a second call over an
// unchanged tree reports RebuildResult.Converged and writes nothing new —
// this function adds no state of its own that could break that guarantee.
func RebuildIndex(ctx context.Context, deps RebuildDeps, opts RebuildOptions) (RebuildIndexResult, error) {
	if err := opts.Corpus.Validate(); err != nil {
		return RebuildIndexResult{}, err
	}
	mgr, err := lifecycle.NewManager(lifecycle.ManagerOptions{
		CatalogPath: deps.CatalogPath,
		Store:       deps.Store,
		Index:       deps.Index,
		Vectors:     deps.Vectors,
		Pipeline:    deps.Pipeline,
		Sources:     rootSourceProvider{corpus: opts.Corpus, roots: opts.Roots},
		TreeHash:    deps.TreeHash,
		Clock:       deps.Clock,
	})
	if err != nil {
		return RebuildIndexResult{}, err
	}
	rebuildResult, err := mgr.Rebuild(ctx)
	if err != nil {
		return RebuildIndexResult{}, err
	}
	verifyReport, err := mgr.Verify(ctx)
	if err != nil {
		return RebuildIndexResult{Rebuild: rebuildResult}, err
	}
	return RebuildIndexResult{Rebuild: rebuildResult, Verify: verifyReport}, nil
}

// rootSourceProvider is the lifecycle.SourceProvider this ticket fills in:
// a directory walk over Roots, one lifecycle.Source per call carrying
// every chunkable file found. It performs no chunking itself (that is
// planSources, inside lifecycle.Manager.Rebuild) and no format-specific
// parsing — it only reads bytes and defers to retrieval.ChunkerFor to
// decide what is chunkable.
type rootSourceProvider struct {
	corpus corpus.Corpus
	roots  []string
}

// Sources implements lifecycle.SourceProvider.
func (p rootSourceProvider) Sources(context.Context) ([]lifecycle.Source, error) {
	var files []lifecycle.SourceFile
	for _, root := range p.roots {
		found, err := walkChunkableFiles(root)
		if err != nil {
			return nil, err
		}
		files = append(files, found...)
	}
	if len(files) == 0 {
		return nil, nil
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return []lifecycle.Source{{Corpus: p.corpus, Files: files}}, nil
}

// walkChunkableFiles reads every file under root that retrieval.ChunkerFor
// recognizes. A root that does not exist is a real, convergent empty
// source (a migration stage that has not written anything there yet), not
// an error; any other stat or read failure is KindUnavailable, because the
// caller cannot tell a permissions problem from an empty directory unless
// this function tells it apart.
func walkChunkableFiles(root string) ([]lifecycle.SourceFile, error) {
	if _, err := os.Stat(root); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, cascade.Wrapf(cascade.KindUnavailable, err,
			"migration v1 recall: stat source root %s", root)
	}
	var out []lifecycle.SourceFile
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if _, chunkerErr := retrieval.ChunkerFor(path); chunkerErr != nil {
			return nil // unsupported extension: not this ticket's corpus, not an error
		}
		content, readErr := os.ReadFile(path) //nolint:gosec // path comes from the caller's own migrated tree
		if readErr != nil {
			return readErr
		}
		out = append(out, lifecycle.SourceFile{Path: path, Content: content})
		return nil
	})
	if walkErr != nil {
		return nil, cascade.Wrapf(cascade.KindUnavailable, walkErr,
			"migration v1 recall: walk source root %s", root)
	}
	return out, nil
}
