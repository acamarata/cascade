package repo

// Purpose: R-21.182 symbol reachability -- the query AC/S-59.T4's change-
//   footprint rule unions with its pre-image/post-image paths: given a set
//   of changed file paths, which packages does the symbol/dependency graph
//   say are reachable from them through an edge whose target package
//   carries an auth, secret or schema sensitivity class.
// Inputs: a *SymbolGraph (the S-67.T3 extractor's output), the changed
//   paths, and the SensitiveClass set to match against.
// Outputs: the sorted, deduplicated set of reachable node IDs whose owning
//   package matches one of classes, or a typed error.
// Constraints: Art.7 -- pure function of graph plus its arguments, no bare
//   time.Now, no I/O. A cycle in the input graph never loops the walk (a
//   visited-set cycle guard, tested against a real 2-node cycle even
//   though a Go import graph is acyclic at package level -- HOW-7's own
//   discipline point). "No reachable sensitive symbols" (success, empty
//   slice) and "could not resolve any of the given paths against this
//   graph" (KindNotFound) are two different answers, never conflated into
//   a silent empty slice for both, mirroring internal/fleet/capacity's
//   Unknown-vs-zero rule for an absent source.
// SPORT: repo/symbol-reachability/ADD (P1-E33-W7-S67-T3).

import (
	"context"
	"sort"

	"github.com/acamarata/cascade/pkg/cascade"
)

// SensitiveClass is the closed R-21.182 vocabulary Reachable matches
// package ownership against. R-21.182's own text names the categories by
// role ("auth, secret or schema") without enumerating the concrete
// directories; S-67.T1's layout scan (internal/repo/layout.go) records no
// per-directory sensitivity marker to read instead (verified: layout.go
// carries only tree-shape counts, no classification field), so this
// ticket derives the mapping itself, mirroring risk.go's own disclosed
// derivation of the R-21.182 Critical-floor categories (P1-E29-W6-S59-T4).
// Narrowing or widening the prefix table needs a T0 ruling, same as that
// precedent.
type SensitiveClass string

// The closed three-member SensitiveClass vocabulary.
const (
	ClassAuth   SensitiveClass = "auth"
	ClassSecret SensitiveClass = "secret"
	ClassSchema SensitiveClass = "schema"
)

// Valid reports whether c is one of the three declared classes.
func (c SensitiveClass) Valid() bool {
	switch c {
	case ClassAuth, ClassSecret, ClassSchema:
		return true
	default:
		return false
	}
}

// classPrefixes maps each SensitiveClass to the repo-relative directory
// prefixes this ticket treats as carrying that class, following the same
// real, already-registered package layout risk.go's criticalFloorMatch
// reads (internal/policy, internal/secrets) plus the elevation/auth
// surface those two packages sit beside.
var classPrefixes = map[SensitiveClass][]string{
	ClassAuth:   {"internal/elevation/", "internal/policy/"},
	ClassSecret: {"internal/secrets/", "internal/hooks/egress/"},
	ClassSchema: {"internal/storage/", "migrations/"},
}

// nodeMatchesClass reports whether n's declaring file or package falls
// under one of classes' directory prefixes.
func nodeMatchesClass(n GraphNode, classes []SensitiveClass) bool {
	for _, c := range classes {
		for _, prefix := range classPrefixes[c] {
			if hasPathPrefix(n.File, prefix) || hasPathPrefix(n.Package, prefix) {
				return true
			}
		}
	}
	return false
}

func hasPathPrefix(p, prefix string) bool {
	if p == "" {
		return false
	}
	for i := 0; i+len(prefix) <= len(p); i++ {
		if p[i:i+len(prefix)] == prefix {
			return true
		}
	}
	return false
}

// Reachability answers R-21.182 reachability queries over one fixed
// SymbolGraph snapshot. The zero value is not usable; build with
// NewReachability.
type Reachability struct {
	graph     *SymbolGraph
	byID      map[string]GraphNode
	byFile    map[string][]string
	adjacency map[string][]string
}

// NewReachability indexes graph for repeated Reachable calls. graph must
// be non-nil; a nil graph is a caller bug (Art.7 -- no permissive default).
func NewReachability(graph *SymbolGraph) (*Reachability, error) {
	if graph == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "repo: reachability requires a non-nil graph")
	}
	r := &Reachability{
		graph:     graph,
		byID:      make(map[string]GraphNode, len(graph.Nodes)),
		byFile:    make(map[string][]string),
		adjacency: make(map[string][]string),
	}
	for _, n := range graph.Nodes {
		r.byID[n.ID] = n
		if n.File != "" {
			r.byFile[n.File] = append(r.byFile[n.File], n.ID)
		}
	}
	for _, e := range graph.Edges {
		r.adjacency[e.From] = append(r.adjacency[e.From], e.To)
	}
	return r, nil
}

// Reachable returns, for the union of nodes declared in paths, every node
// id reachable through graph edges (declares/imports/calls) whose node
// matches one of classes. A path this graph declares no node for
// contributes nothing to the seed set; if NONE of paths resolve, Reachable
// returns a typed KindNotFound error rather than a silent empty success,
// so a caller can tell "resolved, nothing sensitive reachable" apart from
// "this path set does not exist in this graph". classes must be non-empty
// and every member valid.
func (r *Reachability) Reachable(ctx context.Context, paths []string, classes []SensitiveClass) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, cascade.Wrap(cascade.KindCanceled, err, "repo: reachability query canceled")
	}
	if len(classes) == 0 {
		return nil, cascade.New(cascade.KindInvalidInput, "repo: reachability requires at least one sensitive class")
	}
	for _, c := range classes {
		if !c.Valid() {
			return nil, cascade.Newf(cascade.KindInvalidInput, "repo: %q is not a declared sensitive class", string(c))
		}
	}
	seeds, resolved := r.seedsFor(paths)
	if !resolved && len(paths) > 0 {
		return nil, cascade.Newf(cascade.KindNotFound, "repo: none of %d changed path(s) resolve to a node in this graph", len(paths))
	}
	visited := r.walk(seeds)
	var out []string
	for id := range visited {
		if n, ok := r.byID[id]; ok && nodeMatchesClass(n, classes) {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out, nil
}

// seedsFor resolves paths to the node ids declared there. resolved is true
// as soon as at least one path matches a real node, so a caller can
// distinguish "resolved but nothing reachable" from "could not resolve
// any of the given paths" without a second return value.
func (r *Reachability) seedsFor(paths []string) (seeds map[string]bool, resolved bool) {
	seeds = make(map[string]bool)
	for _, p := range paths {
		ids, ok := r.byFile[p]
		if !ok {
			continue
		}
		resolved = true
		for _, id := range ids {
			seeds[id] = true
		}
	}
	return seeds, resolved
}

// walk performs a cycle-safe breadth-first walk from seeds over
// r.adjacency, returning every visited node id (including the seeds
// themselves). A visited-set guard means a cycle in the input -- never
// assumed absent, per HOW-7 -- terminates instead of looping forever.
func (r *Reachability) walk(seeds map[string]bool) map[string]bool {
	visited := make(map[string]bool, len(seeds))
	queue := make([]string, 0, len(seeds))
	for id := range seeds {
		visited[id] = true
		queue = append(queue, id)
	}
	for i := 0; i < len(queue); i++ {
		for _, next := range r.adjacency[queue[i]] {
			if visited[next] {
				continue
			}
			visited[next] = true
			queue = append(queue, next)
		}
	}
	return visited
}
