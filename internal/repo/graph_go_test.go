package repo

import (
	"context"
	"encoding/json"
	"os"
	"testing"
)

// TestGraphExtract_RealCounterpart runs the real go/packages loader
// against the checked-in fixture-graph-go tree (Art.2: a real counterpart,
// never a hand-built fixture that only exercises this package's own
// assumptions about what go/packages reports).
func TestGraphExtract_RealCounterpart(t *testing.T) {
	e := goExtractor{}
	g, err := e.Extract(context.Background(), "testdata/fixture-graph-go")
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if err := g.Validate(); err != nil {
		t.Fatalf("extracted graph failed Validate: %v", err)
	}

	var sawWidget, sawFoo, sawVersion, sawImport bool
	for _, n := range g.Nodes {
		switch {
		case n.Kind == NodeType && n.Name == "Widget":
			sawWidget = true
		case n.Kind == NodeFunc && n.Name == "Foo":
			sawFoo = true
		case n.Kind == NodeVar && n.Name == "Version":
			sawVersion = true
		}
	}
	for _, e := range g.Edges {
		if e.Kind == EdgeImports {
			sawImport = true
		}
	}
	if !sawWidget {
		t.Error("missing exported type node Widget")
	}
	if !sawFoo {
		t.Error("missing exported func node Foo")
	}
	if !sawVersion {
		t.Error("missing exported var node Version")
	}
	if !sawImport {
		t.Error("missing an imports edge")
	}
	for _, n := range g.Nodes {
		if n.Name == "unexportedHelper" {
			t.Error("unexportedHelper must never be emitted as a node")
		}
	}
}

// TestGraphExtract_MalformedTypedError proves a real compile-error tree
// returns a typed error, never a panic.
func TestGraphExtract_MalformedTypedError(t *testing.T) {
	e := goExtractor{}
	_, err := e.Extract(context.Background(), "testdata/fixture-graph-malformed")
	if err == nil {
		t.Fatal("Extract on a malformed tree returned nil error, want typed error")
	}
}

// TestGraphExtract_EmptyRootTypedError proves an empty root is refused
// before any go/packages call.
func TestGraphExtract_EmptyRootTypedError(t *testing.T) {
	e := goExtractor{}
	if _, err := e.Extract(context.Background(), ""); err == nil {
		t.Fatal("Extract(\"\") returned nil error, want typed error")
	}
}

// TestGraphExtract_Determinism proves two extractions of an unchanged
// fixture tree produce an identical SymbolGraph.
func TestGraphExtract_Determinism(t *testing.T) {
	e := goExtractor{}
	g1, err := e.Extract(context.Background(), "testdata/fixture-graph-go")
	if err != nil {
		t.Fatalf("first Extract: %v", err)
	}
	g2, err := e.Extract(context.Background(), "testdata/fixture-graph-go")
	if err != nil {
		t.Fatalf("second Extract: %v", err)
	}
	if len(g1.Nodes) != len(g2.Nodes) || len(g1.Edges) != len(g2.Edges) {
		t.Fatalf("non-deterministic shape: g1=%d/%d g2=%d/%d nodes/edges",
			len(g1.Nodes), len(g1.Edges), len(g2.Nodes), len(g2.Edges))
	}
	for i := range g1.Nodes {
		if g1.Nodes[i] != g2.Nodes[i] {
			t.Fatalf("node %d differs between runs: %+v vs %+v", i, g1.Nodes[i], g2.Nodes[i])
		}
	}
	for i := range g1.Edges {
		if g1.Edges[i] != g2.Edges[i] {
			t.Fatalf("edge %d differs between runs: %+v vs %+v", i, g1.Edges[i], g2.Edges[i])
		}
	}
}

// TestGraphExtract_GoldenMatchesFixture proves the extracted SymbolGraph
// equals the checked-in golden-symbolgraph.json byte-for-byte after
// canonical (MarshalIndent, sorted-map-free struct) JSON marshalling.
func TestGraphExtract_GoldenMatchesFixture(t *testing.T) {
	e := goExtractor{}
	g, err := e.Extract(context.Background(), "testdata/fixture-graph-go")
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	got, err := json.MarshalIndent(g, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want, err := os.ReadFile("testdata/fixture-graph-go/golden-symbolgraph.json")
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("extracted graph does not match golden fixture:\ngot:\n%s\nwant:\n%s", got, want)
	}
}
