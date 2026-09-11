package repo

// Purpose: the SymbolGraph model (nodes/edges), the GraphExtractor
//   interface, and its language registry -- the shape every extractor
//   produces and every consumer (graph_store.go, internal/retrieval/
//   corpus/graph.go) reads, mirroring detector.go's Detector/registry
//   shape for the language-detection family.
// Inputs: none at this layer -- pure types plus the registry function.
// Outputs: SymbolGraph, GraphNode, GraphEdge and their typed enums; the
//   GraphExtractor interface; RegisteredExtractors, the ordered dispatch
//   list.
// Constraints: Art.1.3 -- the registry lists ONLY languages with a real
//   shipped implementation (Go). No js/ts/rust/python/swift entry exists
//   here, and none is claimed anywhere in docs or CLI help.
// SPORT: repo/symbol-dependency-graph/ADD (P1-E33-W7-S67-T3).

import (
	"context"

	"github.com/acamarata/cascade/pkg/cascade"
)

// GraphNodeKind is the closed set of symbol kinds a SymbolGraph node may
// carry. There is no "other" member: an extractor that cannot classify a
// symbol omits it rather than emitting an unclassified node.
type GraphNodeKind string

const (
	// NodePackage is one Go package.
	NodePackage GraphNodeKind = "package"
	// NodeType is one exported type declared in a package.
	NodeType GraphNodeKind = "type"
	// NodeFunc is one exported function or method declared in a package.
	NodeFunc GraphNodeKind = "func"
	// NodeVar is one exported package-level variable or constant.
	NodeVar GraphNodeKind = "var"
)

// Valid reports whether k is one of the four declared node kinds.
func (k GraphNodeKind) Valid() bool {
	switch k {
	case NodePackage, NodeType, NodeFunc, NodeVar:
		return true
	default:
		return false
	}
}

// GraphEdgeKind is the closed set of relationships a SymbolGraph edge may
// carry.
type GraphEdgeKind string

const (
	// EdgeImports connects a package node to a package node it imports.
	EdgeImports GraphEdgeKind = "imports"
	// EdgeDeclares connects a package node to a type/func/var node it
	// declares.
	EdgeDeclares GraphEdgeKind = "declares"
	// EdgeCalls connects a func node to another func node, when
	// go/packages' type-checked call information exposes the reference.
	EdgeCalls GraphEdgeKind = "calls"
)

// Valid reports whether k is one of the three declared edge kinds.
func (k GraphEdgeKind) Valid() bool {
	switch k {
	case EdgeImports, EdgeDeclares, EdgeCalls:
		return true
	default:
		return false
	}
}

// GraphNode is one symbol in a SymbolGraph: a package, an exported type,
// an exported function, or an exported package-level var/const.
type GraphNode struct {
	// ID is a stable, deterministic identifier for this node (package
	// import path for a package node; import-path#Name for the rest).
	ID string `json:"id"`
	// Kind classifies the node.
	Kind GraphNodeKind `json:"kind"`
	// Name is the symbol's declared name (empty for a package node,
	// which is identified by ID alone).
	Name string `json:"name,omitempty"`
	// Package is the import path of the package this node belongs to.
	Package string `json:"package"`
	// File is the path (relative to the scanned root) declaring this
	// node. Empty for a package node with no single declaring file.
	File string `json:"file,omitempty"`
	// Line is the 1-based source line of the declaration. Zero for a
	// package node.
	Line int `json:"line,omitempty"`
}

// Validate reports whether n is well-formed.
func (n GraphNode) Validate() error {
	if n.ID == "" {
		return cascade.New(cascade.KindInvalidInput, "repo: graph node id is required")
	}
	if !n.Kind.Valid() {
		return cascade.Newf(cascade.KindInvalidInput, "repo: %q is not a declared graph node kind", string(n.Kind))
	}
	if n.Package == "" {
		return cascade.New(cascade.KindInvalidInput, "repo: graph node package is required")
	}
	return nil
}

// GraphEdge is one directed relationship between two GraphNode IDs.
type GraphEdge struct {
	// From is the source node ID.
	From string `json:"from"`
	// To is the target node ID.
	To string `json:"to"`
	// Kind classifies the relationship.
	Kind GraphEdgeKind `json:"kind"`
}

// Validate reports whether e is well-formed.
func (e GraphEdge) Validate() error {
	if e.From == "" || e.To == "" {
		return cascade.New(cascade.KindInvalidInput, "repo: graph edge requires non-empty from/to")
	}
	if !e.Kind.Valid() {
		return cascade.Newf(cascade.KindInvalidInput, "repo: %q is not a declared graph edge kind", string(e.Kind))
	}
	return nil
}

// SymbolGraph is the whole symbol/dependency graph extracted from one
// repository root at one scan generation.
type SymbolGraph struct {
	// Nodes is every extracted node, in extractor-emission order (the Go
	// extractor emits deterministic order -- see graph_go.go).
	Nodes []GraphNode `json:"nodes"`
	// Edges is every extracted edge, in extractor-emission order.
	Edges []GraphEdge `json:"edges"`
}

// Validate reports whether g is internally consistent: every node and
// edge is individually well-formed, and every edge references a node ID
// that exists in Nodes.
func (g SymbolGraph) Validate() error {
	ids := make(map[string]bool, len(g.Nodes))
	for _, n := range g.Nodes {
		if err := n.Validate(); err != nil {
			return err
		}
		ids[n.ID] = true
	}
	for _, e := range g.Edges {
		if err := e.Validate(); err != nil {
			return err
		}
		if !ids[e.From] {
			return cascade.Newf(cascade.KindIntegrity, "repo: graph edge references unknown node %q", e.From)
		}
		if !ids[e.To] {
			return cascade.Newf(cascade.KindIntegrity, "repo: graph edge references unknown node %q", e.To)
		}
	}
	return nil
}

// GraphExtractor produces a SymbolGraph for one repository root. Extract
// never panics: a well-formed root with no code for this language returns
// (&SymbolGraph{}, nil); a present-but-malformed source tree returns a
// typed error.
type GraphExtractor interface {
	Extract(ctx context.Context, root string) (*SymbolGraph, error)
}

// graphRegistry is the fixed dispatch order of shipped GraphExtractor
// implementations, keyed by language name. Go is the ONLY shipped
// extractor in this ticket (Art.1.3): the registry names no other
// language, and RegisteredLanguages below is the single place a caller
// checks "is X supported" rather than guessing from registry contents.
func graphRegistry() map[string]GraphExtractor {
	return map[string]GraphExtractor{
		"go": goExtractor{},
	}
}

// RegisteredLanguages returns the sorted, closed set of languages with a
// real shipped GraphExtractor. Currently exactly one member: "go".
func RegisteredLanguages() []string {
	return []string{"go"}
}

// ExtractorFor returns the registered GraphExtractor for language, and
// false if none is registered. A caller must treat false as "not
// supported yet", never fall back to a permissive default extractor.
func ExtractorFor(language string) (GraphExtractor, bool) {
	e, ok := graphRegistry()[language]
	return e, ok
}
