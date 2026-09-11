package repo

// Purpose: turn a sorted []*packages.Package slice into deterministic
//   GraphNode/GraphEdge values -- split from graph_go.go purely to keep
//   both files under the 300-line cap (Art.10.3).
// Inputs: pkgs, already sorted by PkgPath and already checked for load
//   errors by graph_go.go's Extract.
// Outputs: a *SymbolGraph with one node per package/exported type/
//   exported func/exported package-level var and one imports edge per
//   import; declares edges connect each package to what it declares.
// Constraints: deterministic emission order (package, then declares
//   sorted by name, then imports sorted by path) so two extractions of an
//   unchanged tree produce byte-identical output after JSON marshalling.
// SPORT: repo/symbol-dependency-graph/ADD (P1-E33-W7-S67-T3).

import (
	"go/ast"
	"go/token"
	"path/filepath"
	"sort"

	"golang.org/x/tools/go/packages"
)

// emitSymbolGraph walks sorted packages and their type-checked syntax to
// produce the full graph. root relativizes every GraphNode.File so the
// emitted graph (and the golden fixture built from it) carries no
// absolute, checkout-specific path.
func emitSymbolGraph(pkgs []*packages.Package, root string) *SymbolGraph {
	g := &SymbolGraph{}
	for _, p := range pkgs {
		g.Nodes = append(g.Nodes, GraphNode{
			ID:      p.PkgPath,
			Kind:    NodePackage,
			Package: p.PkgPath,
		})

		declared := exportedDecls(p, root)
		sort.Slice(declared, func(i, j int) bool { return declared[i].Name < declared[j].Name })
		for _, d := range declared {
			g.Nodes = append(g.Nodes, GraphNode{
				ID:      p.PkgPath + "#" + d.Name,
				Kind:    d.Kind,
				Name:    d.Name,
				Package: p.PkgPath,
				File:    d.File,
				Line:    d.Line,
			})
			g.Edges = append(g.Edges, GraphEdge{
				From: p.PkgPath,
				To:   p.PkgPath + "#" + d.Name,
				Kind: EdgeDeclares,
			})
		}

		imports := make([]string, 0, len(p.Imports))
		for path := range p.Imports {
			imports = append(imports, path)
		}
		sort.Strings(imports)
		for _, imp := range imports {
			g.Edges = append(g.Edges, GraphEdge{
				From: p.PkgPath,
				To:   imp,
				Kind: EdgeImports,
			})
		}
	}
	return g
}

// declInfo is one exported declaration found while walking a package's
// syntax trees, before it is turned into a GraphNode.
type declInfo struct {
	Name string
	Kind GraphNodeKind
	File string
	Line int
}

// exportedDecls walks p's parsed files (available because NeedSyntax was
// requested) and collects one declInfo per exported top-level type, func,
// or var/const, with File relativized against root.
func exportedDecls(p *packages.Package, root string) []declInfo {
	var out []declInfo
	fset := p.Fset
	for _, file := range p.Syntax {
		for _, decl := range file.Decls {
			out = append(out, declsFromNode(decl, fset, root)...)
		}
	}
	return out
}

// relFile relativizes an absolute source path against root. Falls back
// to the absolute path only if it does not lie under root at all (should
// not happen for anything go/packages loads from this call's own root).
func relFile(root, abs string) string {
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return abs
	}
	return rel
}

// declsFromNode classifies one top-level ast.Decl into zero or more
// exported declInfo entries.
func declsFromNode(decl ast.Decl, fset *token.FileSet, root string) []declInfo {
	var out []declInfo
	switch d := decl.(type) {
	case *ast.FuncDecl:
		if d.Recv != nil || !d.Name.IsExported() {
			// Methods are declared-on their receiver type, not the
			// package directly; this ticket's HOW-2 scopes emission to
			// package-level func/type/var, so a method is skipped here
			// rather than mis-attributed as a free function.
			return nil
		}
		pos := fset.Position(d.Pos())
		out = append(out, declInfo{Name: d.Name.Name, Kind: NodeFunc, File: relFile(root, pos.Filename), Line: pos.Line})
	case *ast.GenDecl:
		for _, spec := range d.Specs {
			out = append(out, declsFromSpec(spec, fset, root)...)
		}
	}
	return out
}

// declsFromSpec classifies one ast.Spec inside a GenDecl (type/var/const
// blocks) into zero or more exported declInfo entries.
func declsFromSpec(spec ast.Spec, fset *token.FileSet, root string) []declInfo {
	var out []declInfo
	switch s := spec.(type) {
	case *ast.TypeSpec:
		if s.Name.IsExported() {
			pos := fset.Position(s.Pos())
			out = append(out, declInfo{Name: s.Name.Name, Kind: NodeType, File: relFile(root, pos.Filename), Line: pos.Line})
		}
	case *ast.ValueSpec:
		for _, name := range s.Names {
			if name.IsExported() {
				pos := fset.Position(name.Pos())
				out = append(out, declInfo{Name: name.Name, Kind: NodeVar, File: relFile(root, pos.Filename), Line: pos.Line})
			}
		}
	}
	return out
}
