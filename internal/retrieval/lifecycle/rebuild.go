// Package lifecycle is the index lifecycle stage of retrieval: the
// rebuild/verify/migrate/update operations over the index the S-10
// sprint built (the FTS5 leg, the embed/vector leg, the corpus/scope
// model and the stable content-addressed chunk ids), plus the doctor
// checks that surface the index's health through `cascade doctor`.
//
// Inputs: a SourceProvider supplying the corpora and files currently
// registered for indexing, the real S-10 FTS5 Indexer and an optional
// embed.Pipeline (nil where no embedder is configured — the same
// degradation registerRecallHandler already documents for the vector
// leg), and provider.Store for this package's own manifest and
// generation-marker bookkeeping.
//
// Outputs: RebuildResult/VerifyReport/MigrateResult/UpdateResult values,
// or a pkg/cascade taxonomy error. Every verb also writes the catalog
// document (recall.CatalogDoc) the query-time recall.FileCatalog reads.
//
// Constraints: no bare time.Now (runtime.Clock only); no direct os/exec —
// the git tree hash is computed by an injected GitTreeHashFunc, whose real
// implementation is the composition root's (internal/daemon/recall_index.go,
// mirroring context_scope.go's gitRootExec).
//
// CONTRACT NOTE (both sides quoted in full in this ticket's journal). This
// ticket's contract describes rebuild as re-running "the S-10 pipeline ...
// over the sources/corpora registered through the S-10 ingest + corpus
// surfaces." The tree has no such registration surface:
// internal/retrieval/chunk.go's own doc comment says chunking "never
// reads from disk itself — the caller (a future ingest-walk ticket)
// supplies bytes already read," and no Source/SourceRegistry type existed
// anywhere in internal/retrieval before this file. The contract's own
// "NOT here" list confirms the config surface that would populate such a
// registration (08-INIT-CONFIG-SPEC §3's [retrieval] sources[]) is
// S-12.T4's. SourceProvider below is the seam a future ingest surface
// fills in; the composition root wires a real-but-empty provider until
// one exists, exactly registerRecallHandler's own "no embedder is
// configured at this composition root yet" pattern. A rebuild with zero
// registered sources is a real, convergent no-op, not a stub.
//
// SPORT: internal.retrieval.lifecycle.Manager/ADDED (P1-E06-W2-S11-T4).
package lifecycle

import (
	"context"
	"sort"

	"github.com/acamarata/cascade/internal/retrieval"
	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/internal/retrieval/embed"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// SourceFile is one file's raw content, already read from disk by
// whatever built the Source (this package never reads a filesystem
// itself; see the package doc's CONTRACT NOTE).
type SourceFile struct {
	// Path identifies the file within its source, carried into every
	// Chunk this file produces.
	Path string
	// Content is the file's raw bytes.
	Content []byte
}

// Source is one corpus of registered content: its classification and the
// files carved from it.
type Source struct {
	// Corpus is this source's classification, validated before any of
	// its Files are chunked.
	Corpus corpus.Corpus
	// Files are the source's files. Order need not be stable; every
	// lifecycle verb re-sorts by Path before writing (Art.7).
	Files []SourceFile
}

// SourceProvider supplies the sources currently registered for indexing.
// The shipped composition-root provider (until S-12.T4 lands) returns an
// empty slice, which every lifecycle verb treats as a real, convergent
// state rather than an error.
type SourceProvider interface {
	Sources(ctx context.Context) ([]Source, error)
}

// GitTreeHashFunc computes the R-21.189 git tree generation marker for the
// current working set. Injected so this package never imports os/exec.
type GitTreeHashFunc func(ctx context.Context) (string, error)

// ManagerOptions configures NewManager.
type ManagerOptions struct {
	// CatalogPath is where the recall.CatalogDoc this package writes is
	// read back by recall.FileCatalog. Required.
	CatalogPath string
	// Store is this package's own bookkeeping store (manifest and
	// generation-marker keys) and the FTS5 leg's backing store, read
	// directly (read-only) by Verify's orphan/missing scan. Required.
	Store provider.Store
	// Index is the FTS5 leg's write side. Required.
	Index retrieval.Indexer
	// Vectors is the vector leg. Nil means no embedder is configured at
	// this composition root; Verify then skips vector-completeness
	// checking rather than reporting a false incompleteness.
	Vectors provider.VectorStore
	// Pipeline embeds and upserts chunks. Nil alongside Vectors.
	Pipeline *embed.Pipeline
	// Sources supplies the registered corpora. Nil behaves as an
	// always-empty provider.
	Sources SourceProvider
	// TreeHash computes the current generation marker. Required.
	TreeHash GitTreeHashFunc
	// Clock timestamps this package's own bookkeeping writes. Required.
	Clock runtime.Clock
}

// Manager owns the index lifecycle operations over one retrieval index.
type Manager struct {
	catalogPath string
	store       provider.Store
	index       retrieval.Indexer
	vectors     provider.VectorStore
	pipeline    *embed.Pipeline
	sources     SourceProvider
	treeHash    GitTreeHashFunc
	clock       runtime.Clock
}

// NewManager validates opts and returns a Manager.
func NewManager(opts ManagerOptions) (*Manager, error) {
	switch {
	case opts.CatalogPath == "":
		return nil, cascade.New(cascade.KindInvalidInput, "lifecycle: no catalog path")
	case opts.Store == nil:
		return nil, cascade.New(cascade.KindInvalidInput, "lifecycle: no store")
	case opts.Index == nil:
		return nil, cascade.New(cascade.KindInvalidInput, "lifecycle: no index")
	case opts.TreeHash == nil:
		return nil, cascade.New(cascade.KindInvalidInput, "lifecycle: no git tree hash function")
	case opts.Clock == nil:
		return nil, cascade.New(cascade.KindInvalidInput, "lifecycle: no clock")
	}
	return &Manager{
		catalogPath: opts.CatalogPath, store: opts.Store, index: opts.Index,
		vectors: opts.Vectors, pipeline: opts.Pipeline, sources: opts.Sources,
		treeHash: opts.TreeHash, clock: opts.Clock,
	}, nil
}

// sourcesOrEmpty returns m.sources' current sources, or an empty slice
// when no provider is configured.
func (m *Manager) sourcesOrEmpty(ctx context.Context) ([]Source, error) {
	if m.sources == nil {
		return nil, nil
	}
	return m.sources.Sources(ctx)
}

// RebuildResult reports one rebuild's outcome.
type RebuildResult struct {
	// CorporaIndexed is how many corpora were (re)written.
	CorporaIndexed int
	// ChunksWritten is how many chunks are new relative to the prior
	// state — the idempotency delta a second, unchanged run reports as 0.
	ChunksWritten int
	// ChunksDeleted is how many previously-indexed chunks no longer
	// appear in the registered sources and were retracted.
	ChunksDeleted int
	// Converged is true when this run changed nothing on either count —
	// the documented second-run-over-unchanged-sources outcome.
	Converged bool
	// Marker is the git tree generation marker this run recorded.
	Marker string
}

// Rebuild fully re-indexes every registered source from a clean slate:
// re-chunk, re-write the FTS5 leg, re-embed via the vector leg (when
// configured), and rewrite the catalog document. Deterministic
// content-addressed ids make it convergent: unchanged chunks are written
// again (Write is idempotent) but counted as neither added nor deleted;
// chunks no longer produced by any source are retracted from both legs.
//
// R-21.189: rebuild is the EXPLICIT repair path, exempt from any
// incremental-only rule (AG/S-67.T4's watcher-driven context index, not
// this one) and is never invoked implicitly. It sets the generation
// marker from the tree it just indexed.
func (m *Manager) Rebuild(ctx context.Context) (RebuildResult, error) {
	sources, err := m.sourcesOrEmpty(ctx)
	if err != nil {
		return RebuildResult{}, err
	}
	plan, err := planSources(sources)
	if err != nil {
		return RebuildResult{}, err
	}
	prevIDs, err := m.allManifestIDs(ctx)
	if err != nil {
		return RebuildResult{}, err
	}
	if err := m.writeCorpora(ctx, plan); err != nil {
		return RebuildResult{}, err
	}
	newIDs := plan.chunkIDSet()
	deleted := diffIDs(prevIDs, newIDs)
	added := diffIDs(newIDs, prevIDs)
	if err := m.retract(ctx, deleted); err != nil {
		return RebuildResult{}, err
	}
	if err := m.replaceManifests(ctx, plan); err != nil {
		return RebuildResult{}, err
	}
	if err := m.writeCatalogFrom(plan); err != nil {
		return RebuildResult{}, err
	}
	marker, err := m.stampMarker(ctx)
	if err != nil {
		return RebuildResult{}, err
	}
	return RebuildResult{
		CorporaIndexed: len(plan.corpora),
		ChunksWritten:  len(added),
		ChunksDeleted:  len(deleted),
		Converged:      len(added) == 0 && len(deleted) == 0,
		Marker:         marker,
	}, nil
}

// stampMarker computes the current tree hash and persists it.
func (m *Manager) stampMarker(ctx context.Context) (string, error) {
	hash, err := m.treeHash(ctx)
	if err != nil {
		return "", cascade.Wrap(cascade.KindUnavailable, err, "lifecycle: compute git tree generation marker")
	}
	if err := writeMarker(ctx, m.store, hash, m.clock.Now()); err != nil {
		return "", err
	}
	return hash, nil
}

// diffIDs returns the ids present in a but not in b, sorted.
func diffIDs(a, b map[string]bool) []string {
	var out []string
	for id := range a {
		if !b[id] {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}
