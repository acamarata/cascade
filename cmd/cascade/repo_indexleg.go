package main

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/acamarata/cascade/internal/repo"
	"github.com/acamarata/cascade/internal/retrieval"
	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/internal/retrieval/embed"
	"github.com/acamarata/cascade/internal/retrieval/embedsel"
	"github.com/acamarata/cascade/internal/retrieval/fusion"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// graphIDsNamespace records, per repository, the graph-corpus chunk ids the
// last WriteGraph indexed, so the next one can retract exactly those.
const graphIDsNamespace = "repo/indexleg/graph"

// repoIndexLeg binds repo.IndexLeg to the full-text index, the embed
// pipeline and the daemon's embedder Selection.
type repoIndexLeg struct {
	idx     *retrieval.Index
	sel     *embedsel.Selection
	vectors provider.VectorStore
	meta    provider.Store
	clock   runtime.Clock
}

var _ repo.IndexLeg = (*repoIndexLeg)(nil)

// newRepoIndexLeg builds the adapter. Every argument is required.
func newRepoIndexLeg(
	idx *retrieval.Index, sel *embedsel.Selection, vectors provider.VectorStore,
	meta provider.Store, clock runtime.Clock,
) (*repoIndexLeg, error) {
	switch {
	case idx == nil:
		return nil, cascade.New(cascade.KindInvalidInput, "indexleg: no index")
	case sel == nil:
		return nil, cascade.New(cascade.KindInvalidInput, "indexleg: no embedder selection")
	case vectors == nil:
		return nil, cascade.New(cascade.KindInvalidInput, "indexleg: no vector store")
	case meta == nil:
		return nil, cascade.New(cascade.KindInvalidInput, "indexleg: no meta store")
	case clock == nil:
		return nil, cascade.New(cascade.KindInvalidInput, "indexleg: no clock")
	}
	return &repoIndexLeg{idx: idx, sel: sel, vectors: vectors, meta: meta, clock: clock}, nil
}

// codeCorpus is the classified code corpus for one repository.
func codeCorpus(repoID string) corpus.Corpus {
	return corpus.Corpus{
		ID:         corpus.CorpusIDCode,
		ScopeRef:   corpus.ScopeRef(repoID),
		Privacy:    corpus.PrivacyProject,
		Visibility: corpus.VisibilityPrivate,
		Trust:      corpus.TrustUntrustedSource,
	}
}

// chunkFile chunks content with the chunker rel's extension selects.
func chunkFile(rel string, content []byte) ([]retrieval.Chunk, error) {
	ch, err := retrieval.ChunkerFor(rel)
	if err != nil {
		return nil, err
	}
	return ch.Chunk(rel, content)
}

// WriteFTS chunks content and indexes it under the code corpus, returning
// the chunk ids in source order.
func (l *repoIndexLeg) WriteFTS(ctx context.Context, repoID, rel string, content []byte) ([]string, error) {
	if repoID == "" || rel == "" {
		return nil, cascade.New(cascade.KindInvalidInput, "indexleg: repository id and path are required")
	}
	chunks, err := chunkFile(rel, content)
	if err != nil {
		return nil, err
	}
	if err := l.idx.Write(ctx, corpus.CorpusIDCode, chunks); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(chunks))
	for _, c := range chunks {
		ids = append(ids, c.ID)
	}
	return ids, nil
}

// DeleteChunks retracts the FTS rows and the code-corpus vectors of
// chunkIDs. Absent ids are no error.
func (l *repoIndexLeg) DeleteChunks(ctx context.Context, _ string, chunkIDs []string) error {
	if len(chunkIDs) == 0 {
		return nil
	}
	if err := l.idx.Delete(ctx, chunkIDs); err != nil {
		return err
	}
	return l.vectors.Delete(ctx, fusion.NamespaceFor(corpus.CorpusIDCode), chunkIDs)
}

// WriteVector embeds the chunks of content named by chunkIDs. It reports
// pending, with no embedder call, when the Selection is unusable or does
// not admit the code corpus's tier.
func (l *repoIndexLeg) WriteVector(
	ctx context.Context, repoID, rel string, chunkIDs []string, content []byte,
) (bool, error) {
	tier := embedsel.PrivacyTier(corpus.PrivacyProject)
	if l.sel.Err() != nil || !l.sel.Admits(tier) {
		return true, nil
	}
	chunks, err := chunkFile(rel, content)
	if err != nil {
		return true, err
	}
	keep := chunks[:0]
	for _, c := range chunks {
		if slices.Contains(chunkIDs, c.ID) {
			keep = append(keep, c)
		}
	}
	if len(keep) == 0 {
		return false, nil
	}
	p, err := embed.New(l.sel.Embedder(), l.vectors, l.meta, l.clock, 0)
	if err != nil {
		return true, err
	}
	if _, err := p.Run(ctx, embed.Request{Corpus: codeCorpus(repoID), Chunks: keep}); err != nil {
		return true, err
	}
	return false, nil
}

// WriteGraph replaces the repository's graph-corpus rows with g's.
func (l *repoIndexLeg) WriteGraph(ctx context.Context, repoID string, g repo.SymbolGraph) error {
	if repoID == "" {
		return cascade.New(cascade.KindInvalidInput, "indexleg: repository id is required")
	}
	_, chunks, err := retrieval.IngestSymbolGraph(&g, corpus.ScopeRef(repoID), corpus.TrustUntrustedSource)
	if err != nil {
		return err
	}
	prior, err := l.priorGraphIDs(ctx, repoID)
	if err != nil {
		return err
	}
	if err := l.idx.Delete(ctx, prior); err != nil {
		return err
	}
	if err := l.idx.Write(ctx, corpus.CorpusIDGraph, chunks); err != nil {
		return err
	}
	ids := make([]string, 0, len(chunks))
	for _, c := range chunks {
		ids = append(ids, c.ID)
	}
	raw, err := json.Marshal(ids)
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "indexleg: encode graph ids")
	}
	return l.meta.Put(ctx, graphIDsNamespace, repoID, raw)
}

// priorGraphIDs reads the ids the last WriteGraph recorded; none is empty.
func (l *repoIndexLeg) priorGraphIDs(ctx context.Context, repoID string) ([]string, error) {
	raw, err := l.meta.Get(ctx, graphIDsNamespace, repoID)
	if err != nil {
		if cascade.HasKind(err, cascade.KindNotFound) {
			return nil, nil
		}
		return nil, err
	}
	var ids []string
	if err := json.Unmarshal(raw, &ids); err != nil {
		return nil, cascade.Wrap(cascade.KindIntegrity, err, "indexleg: decode graph ids")
	}
	return ids, nil
}
