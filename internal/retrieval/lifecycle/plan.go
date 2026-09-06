package lifecycle

// Purpose: turn a []Source into the concrete write plan a lifecycle verb
// executes — chunking every file, building the corpus.Record set the
// catalog document carries, and the per-corpus chunk-id manifest this
// package's own bookkeeping needs (corpus.Record has no Path field, so
// nothing else in the tree can answer "which chunk ids came from path P"
// — see store.go's doc comment for why that answer matters to update).
//
// Inputs: []Source, already validated classification per corpus.
// Outputs: a sourcePlan, or a pkg/cascade taxonomy error from a Chunker or
// a malformed corpus.
// Constraints: pure — no I/O, no store, no index writes. Order is always
// path-then-corpus-id sorted, so two runs over the same sources produce
// byte-identical chunk sequences (Art.7).
//
// SPORT: internal.retrieval.lifecycle.Manager/ADDED (P1-E06-W2-S11-T4).
import (
	"context"
	"sort"

	"github.com/acamarata/cascade/internal/retrieval"
	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/internal/retrieval/embed"
	"github.com/acamarata/cascade/pkg/cascade"
)

// corpusPlan is one corpus's chunked content.
type corpusPlan struct {
	corpus corpus.Corpus
	chunks []retrieval.Chunk
}

// sourcePlan is the whole write plan across every registered source.
type sourcePlan struct {
	corpora []corpus.Corpus
	byID    map[string]corpusPlan
	records []corpus.Record
}

// chunkIDSet returns every chunk id this plan holds, across all corpora.
func (p sourcePlan) chunkIDSet() map[string]bool {
	set := make(map[string]bool)
	for _, cp := range p.byID {
		for _, c := range cp.chunks {
			set[c.ID] = true
		}
	}
	return set
}

// planSources chunks every source's files and builds the catalog record
// set. A source's corpus is validated before its files are chunked, and
// every record inherits its corpus's scope/privacy/visibility/trust
// (this package has no finer-grained per-file classification input; a
// SourceProvider that needs one classifies at the corpus boundary, same
// as every other S-10 consumer of corpus.Corpus).
func planSources(sources []Source) (sourcePlan, error) {
	plan := sourcePlan{byID: make(map[string]corpusPlan)}
	sorted := append([]Source(nil), sources...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Corpus.ID < sorted[j].Corpus.ID })
	for _, src := range sorted {
		if err := src.Corpus.Validate(); err != nil {
			return sourcePlan{}, err
		}
		chunks, err := chunkFiles(src.Files)
		if err != nil {
			return sourcePlan{}, err
		}
		plan.corpora = append(plan.corpora, src.Corpus)
		plan.byID[src.Corpus.ID] = corpusPlan{corpus: src.Corpus, chunks: chunks}
		for _, c := range chunks {
			plan.records = append(plan.records, corpus.Record{
				ID: c.ID, CorpusID: src.Corpus.ID, ScopeRef: src.Corpus.ScopeRef,
				Privacy: src.Corpus.Privacy, Visibility: src.Corpus.Visibility, Trust: src.Corpus.Trust,
			})
		}
	}
	return plan, nil
}

// chunkFiles chunks every file in path order, selecting a Chunker by
// extension (retrieval.ChunkerFor). A file whose extension no chunker
// recognizes is skipped rather than failing the whole rebuild — an
// unrecognized extension is an ordinary corner of "everything under this
// root", not a caller error, and S-10's own ChunkerFor contract reserves
// KindUnsupported for exactly this case.
func chunkFiles(files []SourceFile) ([]retrieval.Chunk, error) {
	sorted := append([]SourceFile(nil), files...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })
	var out []retrieval.Chunk
	for _, f := range sorted {
		chunker, err := retrieval.ChunkerFor(f.Path)
		if err != nil {
			if cascade.HasKind(err, cascade.KindUnsupported) {
				continue
			}
			return nil, err
		}
		chunks, err := chunker.Chunk(f.Path, f.Content)
		if err != nil {
			return nil, err
		}
		out = append(out, chunks...)
	}
	return out, nil
}

// writeCorpora writes every corpus's chunks to the FTS5 leg and, when a
// pipeline is configured, embeds and upserts them into the vector leg.
func (m *Manager) writeCorpora(ctx context.Context, plan sourcePlan) error {
	for _, id := range sortedCorpusIDs(plan) {
		cp := plan.byID[id]
		if err := m.index.Write(ctx, id, cp.chunks); err != nil {
			return err
		}
		if m.pipeline == nil || len(cp.chunks) == 0 {
			continue
		}
		if _, err := m.pipeline.Run(ctx, embed.Request{Corpus: cp.corpus, Chunks: cp.chunks}); err != nil {
			return err
		}
	}
	return nil
}

// sortedCorpusIDs returns plan's corpus ids in ascending order (Art.7:
// map iteration never reaches a write path directly).
func sortedCorpusIDs(plan sourcePlan) []string {
	ids := make([]string, 0, len(plan.byID))
	for id := range plan.byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
