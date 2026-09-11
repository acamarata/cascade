package retrieval

// Purpose: the "graph" corpus type -- serializes a repo symbol/dependency
//   SymbolGraph into one short, provenance-carrying text record per node
//   and edge, for the existing F/S-10.T2 FTS5 and F/S-10.T3 embed/vector
//   upsert paths (fts5.go's Index.Write, embed.Pipeline.Run). No new
//   chunker: each record is already atomic, unlike code.go's chunked
//   source text.
//
// This ticket's files_scope names internal/retrieval/corpus/graph.go as
// the intended location, matching P1-E25-W5-S52-T6's own files_scope for
// code.go. That location is unreachable for the same reason T6's journal
// records: internal/retrieval/corpus is a leaf package internal/
// retrieval's own fusion sub-package already imports (retrieval ->
// fusion -> corpus), so corpus importing retrieval (for the Chunk type
// and internal/repo.SymbolGraph) closes a cycle the compiler refuses.
// This file lives in package retrieval instead, alongside gitcorpus.go,
// which already establishes this exact placement for the sibling "code"
// corpus. corpus/registry.go still carries the kind registration
// (CorpusIDGraph), which has no such dependency and stays at the
// ticket's named path.
//
// Inputs: a *repo.SymbolGraph (internal/repo/graph.go) and the caller's
//   chosen corpus.ScopeRef and corpus.TrustLevel -- never hardcoded here,
//   propagated exactly as given (mirrors gitcorpus.go's trust-propagation
//   rule and its own test).
//
// Outputs: a validated corpus.Corpus{ID: corpus.CorpusIDGraph} plus one
//   Chunk per GraphNode and one per GraphEdge. Writes nothing to any
//   store itself.
//
// SPORT: internal.retrieval.IngestSymbolGraph/ADDED (P1-E33-W7-S67-T3).

import (
	"fmt"

	"github.com/acamarata/cascade/internal/repo"
	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/pkg/cascade"
)

// graphChunkLang tags every graph-corpus Chunk's Lang field, distinguishing
// it from a code.go chunk ("go", "markdown", ...) sharing the same FTS5/
// vector store.
const graphChunkLang = "graph"

// IngestSymbolGraph serializes g into one provenance-carrying text record
// per node and per edge, returning the classified corpus.Corpus and the
// ordered []Chunk for the existing ingest pipeline to write. trust is
// threaded through unchanged (F/S-10.T4): this function never hardcodes
// "trusted".
func IngestSymbolGraph(
	g *repo.SymbolGraph, scopeRef corpus.ScopeRef, trust corpus.TrustLevel,
) (corpus.Corpus, []Chunk, error) {
	if err := corpus.ValidateCorpusKind(corpus.CorpusIDGraph); err != nil {
		return corpus.Corpus{}, nil, err
	}
	if g == nil {
		return corpus.Corpus{}, nil, cascade.New(cascade.KindInvalidInput, "retrieval: nil symbol graph")
	}
	if err := g.Validate(); err != nil {
		return corpus.Corpus{}, nil, err
	}
	c := corpus.Corpus{
		ID:         corpus.CorpusIDGraph,
		ScopeRef:   scopeRef,
		Privacy:    corpus.PrivacyProject,
		Visibility: corpus.VisibilityPrivate,
		Trust:      trust,
	}
	if err := c.Validate(); err != nil {
		return corpus.Corpus{}, nil, err
	}

	chunks := make([]Chunk, 0, len(g.Nodes)+len(g.Edges))
	for _, n := range g.Nodes {
		chunks = append(chunks, graphNodeChunk(n))
	}
	for _, e := range g.Edges {
		chunks = append(chunks, graphEdgeChunk(e))
	}
	return c, chunks, nil
}

// graphNodeChunk renders one GraphNode as a short descriptive text record
// carrying its provenance (node id, file, line) in the record text itself,
// per HOW-4's "provenance-carrying" requirement.
func graphNodeChunk(n repo.GraphNode) Chunk {
	var text string
	switch n.Kind {
	case repo.NodePackage:
		text = fmt.Sprintf("package %s", n.Package)
	case repo.NodeType, repo.NodeFunc, repo.NodeVar:
		text = fmt.Sprintf("%s %s in package %s (%s:%d)", n.Kind, n.Name, n.Package, n.File, n.Line)
	default:
		text = fmt.Sprintf("%s %s in package %s (%s:%d)", n.Kind, n.Name, n.Package, n.File, n.Line)
	}
	content := []byte(text)
	return Chunk{
		ID:      ChunkID(content),
		Path:    n.File,
		Content: content,
		Lang:    graphChunkLang,
	}
}

// graphEdgeChunk renders one GraphEdge as a short descriptive text record,
// e.g. "func Foo in pkg/x imports pkg/y" for an imports edge between two
// package nodes.
func graphEdgeChunk(e repo.GraphEdge) Chunk {
	text := fmt.Sprintf("%s %s %s", e.From, e.Kind, e.To)
	content := []byte(text)
	return Chunk{
		ID:      ChunkID(content),
		Content: content,
		Lang:    graphChunkLang,
	}
}
