// Package retrieval (this file) tests the graph-corpus type: kind
// registration, provenance-carrying serialization, and trust
// propagation. The real go/packages extraction itself is Art.2-tested in
// internal/repo/graph_go_test.go; this file exercises IngestSymbolGraph's
// own serialization contract against a SymbolGraph value, mirroring
// gitcorpus_test.go's structure for the sibling "code" corpus.
//
// SPORT: internal.retrieval.IngestSymbolGraph/ADDED test coverage
// (P1-E33-W7-S67-T3).
package retrieval

import (
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/repo"
	"github.com/acamarata/cascade/internal/retrieval/corpus"
)

func testGraph() *repo.SymbolGraph {
	return &repo.SymbolGraph{
		Nodes: []repo.GraphNode{
			{ID: "pkg/x", Kind: repo.NodePackage, Package: "pkg/x"},
			{ID: "pkg/x#Foo", Kind: repo.NodeFunc, Name: "Foo", Package: "pkg/x", File: "x.go", Line: 3},
			{ID: "pkg/y", Kind: repo.NodePackage, Package: "pkg/y"},
		},
		Edges: []repo.GraphEdge{
			{From: "pkg/x", To: "pkg/x#Foo", Kind: repo.EdgeDeclares},
			{From: "pkg/x", To: "pkg/y", Kind: repo.EdgeImports},
		},
	}
}

func TestIngestSymbolGraph_RecordsPerNodeAndEdge(t *testing.T) {
	g := testGraph()
	c, chunks, err := IngestSymbolGraph(g, "scope-a", corpus.TrustTrusted)
	if err != nil {
		t.Fatalf("IngestSymbolGraph: %v", err)
	}
	if c.ID != corpus.CorpusIDGraph {
		t.Fatalf("corpus.ID = %q, want %q", c.ID, corpus.CorpusIDGraph)
	}
	wantCount := len(g.Nodes) + len(g.Edges)
	if len(chunks) != wantCount {
		t.Fatalf("len(chunks) = %d, want %d", len(chunks), wantCount)
	}
	for _, ch := range chunks {
		if ch.Lang != graphChunkLang {
			t.Errorf("chunk Lang = %q, want %q", ch.Lang, graphChunkLang)
		}
		if ch.ID == "" {
			t.Error("chunk ID is empty")
		}
	}
}

// TestIngestSymbolGraph_ProvenanceCarrying proves each record's text
// itself carries provenance (the node/edge identity), per HOW-4.
func TestIngestSymbolGraph_ProvenanceCarrying(t *testing.T) {
	g := testGraph()
	_, chunks, err := IngestSymbolGraph(g, "scope-a", corpus.TrustTrusted)
	if err != nil {
		t.Fatalf("IngestSymbolGraph: %v", err)
	}
	var sawFooText, sawEdgeText bool
	for _, ch := range chunks {
		text := string(ch.Content)
		if strings.Contains(text, "Foo") && strings.Contains(text, "pkg/x") {
			sawFooText = true
		}
		if strings.Contains(text, "imports") && strings.Contains(text, "pkg/y") {
			sawEdgeText = true
		}
	}
	if !sawFooText {
		t.Error("no chunk text carries the Foo node's provenance")
	}
	if !sawEdgeText {
		t.Error("no chunk text carries the imports edge's provenance")
	}
}

// TestIngestSymbolGraph_UntrustedPropagates mirrors gitcorpus_test.go's
// trust-propagation test: the function never hardcodes "trusted".
func TestIngestSymbolGraph_UntrustedPropagates(t *testing.T) {
	c, _, err := IngestSymbolGraph(testGraph(), "scope-a", corpus.TrustUntrustedSource)
	if err != nil {
		t.Fatalf("IngestSymbolGraph: %v", err)
	}
	if c.Trust != corpus.TrustUntrustedSource {
		t.Fatalf("c.Trust = %q, want %q", c.Trust, corpus.TrustUntrustedSource)
	}
}

func TestIngestSymbolGraph_NilGraphRefused(t *testing.T) {
	if _, _, err := IngestSymbolGraph(nil, "scope-a", corpus.TrustTrusted); err == nil {
		t.Fatal("IngestSymbolGraph(nil, ...): err = nil, want typed error")
	}
}

func TestIngestSymbolGraph_InvalidGraphRefused(t *testing.T) {
	bad := &repo.SymbolGraph{Edges: []repo.GraphEdge{{From: "a", To: "b", Kind: repo.EdgeImports}}}
	if _, _, err := IngestSymbolGraph(bad, "scope-a", corpus.TrustTrusted); err == nil {
		t.Fatal("IngestSymbolGraph with a dangling edge: err = nil, want typed error")
	}
}

func TestIngestSymbolGraph_InvalidScopeRefused(t *testing.T) {
	if _, _, err := IngestSymbolGraph(testGraph(), "", corpus.TrustTrusted); err == nil {
		t.Fatal("IngestSymbolGraph with an empty scope ref: err = nil, want typed error")
	}
}

func TestCorpusIDGraph_Registered(t *testing.T) {
	if err := corpus.ValidateCorpusKind(corpus.CorpusIDGraph); err != nil {
		t.Fatalf("ValidateCorpusKind(CorpusIDGraph): %v", err)
	}
}
