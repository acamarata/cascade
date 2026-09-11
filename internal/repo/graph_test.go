package repo

import "testing"

func TestGraphNodeKindValid(t *testing.T) {
	valid := []GraphNodeKind{NodePackage, NodeType, NodeFunc, NodeVar}
	for _, k := range valid {
		if !k.Valid() {
			t.Errorf("GraphNodeKind(%q).Valid() = false, want true", k)
		}
	}
	if GraphNodeKind("other").Valid() {
		t.Error(`GraphNodeKind("other").Valid() = true, want false`)
	}
}

func TestGraphEdgeKindValid(t *testing.T) {
	valid := []GraphEdgeKind{EdgeImports, EdgeDeclares, EdgeCalls}
	for _, k := range valid {
		if !k.Valid() {
			t.Errorf("GraphEdgeKind(%q).Valid() = false, want true", k)
		}
	}
	if GraphEdgeKind("other").Valid() {
		t.Error(`GraphEdgeKind("other").Valid() = true, want false`)
	}
}

func TestGraphNodeValidate(t *testing.T) {
	cases := []struct {
		name    string
		node    GraphNode
		wantErr bool
	}{
		{"valid", GraphNode{ID: "pkg", Kind: NodePackage, Package: "pkg"}, false},
		{"missing id", GraphNode{Kind: NodePackage, Package: "pkg"}, true},
		{"bad kind", GraphNode{ID: "pkg", Kind: "bogus", Package: "pkg"}, true},
		{"missing package", GraphNode{ID: "pkg", Kind: NodePackage}, true},
	}
	for _, c := range cases {
		err := c.node.Validate()
		if (err != nil) != c.wantErr {
			t.Errorf("%s: Validate() err=%v, wantErr=%v", c.name, err, c.wantErr)
		}
	}
}

func TestGraphEdgeValidate(t *testing.T) {
	cases := []struct {
		name    string
		edge    GraphEdge
		wantErr bool
	}{
		{"valid", GraphEdge{From: "a", To: "b", Kind: EdgeImports}, false},
		{"missing from", GraphEdge{To: "b", Kind: EdgeImports}, true},
		{"missing to", GraphEdge{From: "a", Kind: EdgeImports}, true},
		{"bad kind", GraphEdge{From: "a", To: "b", Kind: "bogus"}, true},
	}
	for _, c := range cases {
		err := c.edge.Validate()
		if (err != nil) != c.wantErr {
			t.Errorf("%s: Validate() err=%v, wantErr=%v", c.name, err, c.wantErr)
		}
	}
}

func TestSymbolGraphValidate(t *testing.T) {
	g := SymbolGraph{
		Nodes: []GraphNode{{ID: "pkg", Kind: NodePackage, Package: "pkg"}},
		Edges: []GraphEdge{{From: "pkg", To: "missing", Kind: EdgeImports}},
	}
	if err := g.Validate(); err == nil {
		t.Fatal("Validate() = nil, want error for edge referencing unknown node")
	}
}

func TestSymbolGraphValidateEmptyOK(t *testing.T) {
	if err := (SymbolGraph{}).Validate(); err != nil {
		t.Fatalf("Validate() on empty graph = %v, want nil", err)
	}
}

func TestRegisteredLanguagesGoOnly(t *testing.T) {
	langs := RegisteredLanguages()
	if len(langs) != 1 || langs[0] != "go" {
		t.Fatalf("RegisteredLanguages() = %v, want [go]", langs)
	}
	if _, ok := ExtractorFor("rust"); ok {
		t.Error(`ExtractorFor("rust") ok=true, want false: no rust extractor is shipped`)
	}
	if _, ok := ExtractorFor("go"); !ok {
		t.Error(`ExtractorFor("go") ok=false, want true`)
	}
}
