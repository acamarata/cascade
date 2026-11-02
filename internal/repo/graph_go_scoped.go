package repo

// Purpose: load a SUBSET of a Go module's packages and merge the result
//   into an already stored SymbolGraph, so steady-state maintenance
//   re-extracts only the packages a change touched (contract:repo-maintenance).
// Inputs: a module root, repo-relative package directories (slash form),
//   and the prior stored graph.
// Outputs: the subset's SymbolGraph; or the prior graph with the reloaded
//   packages replaced, sorted exactly as a full Extract orders its output so
//   untouched packages stay byte-identical.
// Constraints: no "./..." load here; a compile error in a reloaded package
//   is a typed refusal, as in Extract. Imports of packages outside the graph
//   are dropped because SymbolGraph.Validate requires both edge ends to be
//   nodes.
// SPORT: repo/symbol-dependency-graph/ADD (P1-GEN-04).

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"golang.org/x/mod/modfile"

	"github.com/acamarata/cascade/pkg/cascade"
)

// packageExtractor is implemented by extractors that can load a package
// subset. The graph leg reaches it through ExtractorFor.
type packageExtractor interface {
	ExtractPackages(ctx context.Context, root string, dirs []string) (*SymbolGraph, error)
}

// ExtractPackages loads only the packages in dirs (repo-relative, slash
// form; "." is the root package) and emits their SymbolGraph.
func (goExtractor) ExtractPackages(ctx context.Context, root string, dirs []string) (*SymbolGraph, error) {
	if len(dirs) == 0 {
		return &SymbolGraph{}, nil
	}
	patterns := make([]string, 0, len(dirs))
	for _, d := range dirs {
		d = path.Clean(filepath.ToSlash(d))
		if d == "." {
			patterns = append(patterns, ".")
			continue
		}
		if strings.HasPrefix(d, "..") || path.IsAbs(d) {
			return nil, cascade.Newf(cascade.KindInvalidInput, "repo: package dir %q escapes the root", d)
		}
		patterns = append(patterns, "./"+d)
	}
	sort.Strings(patterns)
	return loadGraph(ctx, root, patterns)
}

// packageOf names the package a node or edge-end id belongs to.
func packageOf(id string) string {
	if i := strings.IndexByte(id, '#'); i >= 0 {
		return id[:i]
	}
	return id
}

// MergePackages returns prior with the packages named in reloaded (import
// paths) replaced by part. Nodes sort by package then name and edges group
// by the package of their source, the order a full Extract emits.
func MergePackages(prior SymbolGraph, reloaded []string, part SymbolGraph) SymbolGraph {
	drop := make(map[string]bool, len(reloaded))
	for _, p := range reloaded {
		drop[p] = true
	}
	var out SymbolGraph
	ids := map[string]bool{}
	for _, n := range prior.Nodes {
		if !drop[n.Package] {
			out.Nodes = append(out.Nodes, n)
			ids[n.ID] = true
		}
	}
	for _, n := range part.Nodes {
		out.Nodes = append(out.Nodes, n)
		ids[n.ID] = true
	}
	for _, e := range prior.Edges {
		if !drop[packageOf(e.From)] {
			out.Edges = append(out.Edges, e)
		}
	}
	out.Edges = append(out.Edges, part.Edges...)
	out.Edges = keepResolved(out.Edges, ids)
	sort.SliceStable(out.Nodes, func(i, j int) bool {
		a, b := out.Nodes[i], out.Nodes[j]
		if a.Package != b.Package {
			return a.Package < b.Package
		}
		return a.Name < b.Name
	})
	sort.SliceStable(out.Edges, func(i, j int) bool {
		return packageOf(out.Edges[i].From) < packageOf(out.Edges[j].From)
	})
	return out
}

// keepResolved drops edges whose ends are not both nodes.
func keepResolved(edges []GraphEdge, ids map[string]bool) []GraphEdge {
	var kept []GraphEdge
	for _, e := range edges {
		if ids[e.From] && ids[e.To] {
			kept = append(kept, e)
		}
	}
	return kept
}

// modulePath reads the module path from root/go.mod.
func modulePath(root string) (string, error) {
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return "", cascade.Wrap(cascade.KindUnavailable, err, "repo: read go.mod")
	}
	if mod := modfile.ModulePath(data); mod != "" {
		return mod, nil
	}
	return "", cascade.New(cascade.KindInvalidInput, "repo: go.mod declares no module path")
}

// packageDirLoadable reports whether dir (repo-relative) holds a non-test
// Go file, i.e. whether go/packages would yield a package for it.
func packageDirLoadable(root, dir string) bool {
	ents, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(dir)))
	if err != nil {
		return false
	}
	for _, e := range ents {
		n := e.Name()
		if !e.IsDir() && strings.HasSuffix(n, ".go") && !strings.HasSuffix(n, "_test.go") {
			return true
		}
	}
	return false
}

// scopedGraph rebuilds the stored graph for the package holding the Go file
// rel: it reloads that package alone (or just drops it when no loadable Go
// file remains) and merges into prior.
func scopedGraph(ctx context.Context, root, rel string, prior SymbolGraph) (SymbolGraph, error) {
	ex, ok := ExtractorFor("go")
	pe, isPkg := ex.(packageExtractor)
	if !ok || !isPkg {
		return SymbolGraph{}, cascade.New(cascade.KindUnsupported, "repo: the go extractor cannot load a package subset")
	}
	mod, err := modulePath(root)
	if err != nil {
		return SymbolGraph{}, err
	}
	dir := path.Dir(rel)
	pkg := mod
	if dir != "." {
		pkg = mod + "/" + dir
	}
	var part SymbolGraph
	if packageDirLoadable(root, dir) {
		g, lerr := pe.ExtractPackages(ctx, root, []string{dir})
		if lerr != nil {
			return SymbolGraph{}, lerr
		}
		part = *g
	}
	return MergePackages(prior, []string{pkg}, part), nil
}

// graphLanguage returns the registered graph language rel belongs to, or "".
func graphLanguage(rel string) string {
	if path.Ext(rel) != ".go" {
		return ""
	}
	for _, l := range RegisteredLanguages() {
		if l == "go" {
			return l
		}
	}
	return ""
}

// legGraph rebuilds only the changed Go file's package in the stored graph.
func (m *Maintenance) legGraph(ctx context.Context, p *pathRun) error {
	if graphLanguage(p.rel) == "" {
		return nil
	}
	if _, err := os.Stat(filepath.Join(m.d.Root, "go.mod")); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil // not a Go module: there is no graph to maintain
		}
		return cascade.Wrap(cascade.KindUnavailable, err, "repo: stat go.mod")
	}
	prior, gen, ok, err := m.d.Graph.Get(ctx, m.d.RepositoryID)
	if err != nil {
		return err
	}
	merged, err := scopedGraph(ctx, m.d.Root, p.rel, prior)
	if err != nil {
		return err
	}
	if !ok || !sameGraph(prior, merged) {
		if err := m.d.Graph.Upsert(ctx, m.d.RepositoryID, gen+1, merged, m.d.Clock.Now().Unix()); err != nil {
			return err
		}
	}
	return m.d.Index.WriteGraph(ctx, m.d.RepositoryID, merged)
}

func sameGraph(a, b SymbolGraph) bool {
	return len(a.Nodes) == len(b.Nodes) && len(a.Edges) == len(b.Edges) &&
		(len(a.Nodes) == 0 || reflect.DeepEqual(a.Nodes, b.Nodes)) &&
		(len(a.Edges) == 0 || reflect.DeepEqual(a.Edges, b.Edges))
}
