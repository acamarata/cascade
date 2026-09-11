package repo

// Purpose: the ONE shipped GraphExtractor implementation (Go), using the
//   real golang.org/x/tools/go/packages loader -- 06-FORGE-SPEC §7's
//   standing-authorized license-gated dependency addition, registered in
//   internal/build/licenses.go in this same change. This file owns the
//   packages.Load call and its config; graph_go_emit.go owns turning the
//   loaded *packages.Package slice into GraphNode/GraphEdge values, so
//   neither file exceeds the 300-line cap.
// Inputs: a repository root directory (a Go module root: go.mod present).
// Outputs: a *SymbolGraph, or a typed error for a load failure or a
//   package carrying real compile errors (a malformed source tree is
//   never silently skipped).
// Constraints: deterministic -- packages.Load's own package order is not
//   guaranteed stable across runs, so emission sorts packages by import
//   path before walking them (graph_go_emit.go). No CGO; pure Go.
// SPORT: repo/symbol-dependency-graph/ADD (P1-E33-W7-S67-T3).

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"

	"golang.org/x/tools/go/packages"

	"github.com/acamarata/cascade/pkg/cascade"
)

// goPackagesLoadMode extends 19's HOW-2 named mode
// (NeedName|NeedFiles|NeedImports|NeedTypes|NeedTypesInfo) with
// NeedSyntax. CONTRACT DEVIATION, disclosed: the named mode alone leaves
// every Package.Syntax slice empty (go/packages never parses without
// NeedSyntax, regardless of NeedTypes/NeedTypesInfo), so graph_go_emit.go
// has no ast.Decl to classify and HOW-2's own requirement ("emit one node
// per exported type/func/var") is unreachable without it. Verified by
// running the named mode alone against the fixture tree: p.Syntax was
// empty for both packages. Adding NeedSyntax carries no new dependency
// and no behavior beyond what HOW-2 already specifies as the output.
const goPackagesLoadMode = packages.NeedName |
	packages.NeedFiles |
	packages.NeedImports |
	packages.NeedTypes |
	packages.NeedTypesInfo |
	packages.NeedSyntax

// goExtractor is the Go-language GraphExtractor, registered under "go" in
// graphRegistry(). The zero value is usable: packages.Load needs no
// injected state beyond the root it is pointed at.
type goExtractor struct{}

var _ GraphExtractor = goExtractor{}

// Extract loads every package under root (recursively, "./...") with
// goPackagesLoadMode and emits one SymbolGraph.
//
// A package.Errors entry (a real compile error -- an unresolvable import,
// a syntax error) is a typed refusal, never a silently-skipped package:
// a caller must not mistake a partial graph for a complete one.
func (goExtractor) Extract(ctx context.Context, root string) (*SymbolGraph, error) {
	if root == "" {
		return nil, cascade.New(cascade.KindInvalidInput, "repo: graph extraction requires a non-empty root")
	}
	cfg := &packages.Config{
		Context: ctx,
		Dir:     root,
		Mode:    goPackagesLoadMode,
	}
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		return nil, cascade.Wrapf(cascade.KindUnavailable, err, "repo: go/packages load of %q", root)
	}
	if len(pkgs) == 0 {
		return &SymbolGraph{}, nil
	}

	var loadErrs []string
	for _, p := range pkgs {
		for _, e := range p.Errors {
			loadErrs = append(loadErrs, fmt.Sprintf("%s: %s", p.PkgPath, e.Error()))
		}
	}
	if len(loadErrs) > 0 {
		sort.Strings(loadErrs)
		return nil, cascade.Newf(cascade.KindInvalidInput,
			"repo: go/packages reported %d error(s) under %q: %s", len(loadErrs), root, loadErrs[0])
	}

	sort.Slice(pkgs, func(i, j int) bool { return pkgs[i].PkgPath < pkgs[j].PkgPath })
	absRoot, aerr := filepath.Abs(root)
	if aerr != nil {
		absRoot = root
	}
	return emitSymbolGraph(pkgs, absRoot), nil
}
