package repo

// Purpose: R-21.190 cross-scope graph-edge gating. A symbol/dependency
//   edge is inherently cross-repository (repo A importing repo B's
//   package produces a real edge), so this file stores every such edge
//   -- Reachable and GraphExtractor never drop one -- but makes it
//   TRAVERSABLE only when the E/S-08.T4 scope graph holds a depends_on or
//   shares_context_with edge between the two scopes. The bounded walk
//   applies the check at EVERY hop, never once at the end.
// Inputs: a *SymbolGraph, a seed node id set, the walking session's own
//   scope.Ref, a NodeOwner (which scope owns a given node), and a
//   *CrossScopeGate over the real scope.GraphStore.
// Outputs: CrossScopeWalkResult -- every node id reached plus every hop
//   the gate refused (a refusal is recorded, never silently dropped).
// Constraints: R-16.4/R-16.49 -- ScopeFilter (here, the gate check) runs
//   BEFORE a branch is followed, never post-rank; a hop with no
//   permitting edge terminates that branch only, not the whole walk.
//   member_of and the parent chain are deliberately not consulted:
//   R-21.190 names depends_on/shares_context_with by role, and widening
//   to the traversal package's other edge classes would let an ordinary
//   membership or ancestry relationship silently open a cross-repo reach
//   it was never declared for.
// SPORT: repo/graph-cross-scope-gating/ADD (P1-E33-W7-S67-T3).

import (
	"context"

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/pkg/cascade"
)

// NodeOwner reports which scope owns the node identified by id, and
// whether that ownership is known. ok=false means "this node belongs to
// the walking session's own scope" -- the case for every node the Go
// extractor emits today, since Extract walks exactly one repository root
// per call. A caller wiring multiple repositories' graphs together
// supplies a real NodeOwner; this mirrors ReachabilityFn's nil-until-
// wired posture (R-21.257) rather than inventing a federation mechanism
// this ticket does not need.
type NodeOwner func(id string) (owner scope.Ref, ok bool)

// CrossScopeGate answers "may a walk starting in own's scope cross into
// target's scope" against the real E/S-08.T4 scope graph.
type CrossScopeGate struct {
	store *scope.GraphStore
}

// newCrossScopeGate builds a gate over store. store must be non-nil.
func newCrossScopeGate(store *scope.GraphStore) (*CrossScopeGate, error) {
	if store == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "repo: cross-scope gate requires a non-nil scope store")
	}
	return &CrossScopeGate{store: store}, nil
}

// crossScopeEdgeKinds is the exact, closed set of persisted EdgeKinds
// R-21.190 names as permitting a cross-scope hop.
var crossScopeEdgeKinds = []scope.EdgeKind{scope.EdgeKindDependsOn, scope.EdgeKindSharesContextWith}

// Permits reports whether own may cross into target, per R-21.190: only
// when a depends_on or shares_context_with edge from own to target exists
// in the real scope graph. The same scope always permits itself.
func (g *CrossScopeGate) Permits(ctx context.Context, own, target scope.Ref) (bool, error) {
	if own == target {
		return true, nil
	}
	for _, kind := range crossScopeEdgeKinds {
		targets, err := g.store.EdgeTargets(ctx, own, kind)
		if err != nil {
			return false, err
		}
		for _, t := range targets {
			if t == target {
				return true, nil
			}
		}
	}
	return false, nil
}

// Refusal records one hop the walk terminated without crossing -- the
// R-21.190 acceptance's "recorded as a refusal, not silently dropped".
type Refusal struct {
	NodeID string
	Owner  scope.Ref
}

// CrossScopeWalkResult is gatedWalk's output.
type CrossScopeWalkResult struct {
	// Reached is every node id the walk visited, seeds included.
	Reached []string
	// Refusals is every hop the gate denied, in the order encountered.
	Refusals []Refusal
}

// gatedWalk performs a bounded, cycle-safe walk of graph's edges starting
// from seeds. At every hop it resolves the target node's owner; if the
// owner differs from own, it consults gate.Permits BEFORE following that
// edge, never after. A denied hop is recorded as a Refusal and that
// branch stops there; the walk continues on every other branch. A cycle
// in graph (never assumed absent, per HOW-7) cannot loop the walk: a
// visited node is never re-queued.
func gatedWalk(ctx context.Context, graph *SymbolGraph, seeds []string, own scope.Ref, owner NodeOwner, gate *CrossScopeGate) (CrossScopeWalkResult, error) {
	if graph == nil {
		return CrossScopeWalkResult{}, cascade.New(cascade.KindInvalidInput, "repo: gated walk requires a non-nil graph")
	}
	if gate == nil {
		return CrossScopeWalkResult{}, cascade.New(cascade.KindInvalidInput, "repo: gated walk requires a non-nil cross-scope gate")
	}
	adjacency := buildAdjacency(graph)
	visited := make(map[string]bool)
	var result CrossScopeWalkResult
	queue := make([]string, 0, len(seeds))
	for _, id := range seeds {
		if visited[id] {
			continue
		}
		visited[id] = true
		result.Reached = append(result.Reached, id)
		queue = append(queue, id)
	}
	for i := 0; i < len(queue); i++ {
		next, err := stepFrom(ctx, queue[i], adjacency, visited, own, owner, gate)
		if err != nil {
			return CrossScopeWalkResult{}, err
		}
		result.Reached = append(result.Reached, next.reached...)
		result.Refusals = append(result.Refusals, next.refusals...)
		queue = append(queue, next.reached...)
	}
	return result, nil
}

// stepResult is one node's expansion during gatedWalk, split out purely
// to keep gatedWalk itself under the 50-line function cap.
type stepResult struct {
	reached  []string
	refusals []Refusal
}

func stepFrom(ctx context.Context, from string, adjacency map[string][]string, visited map[string]bool, own scope.Ref, owner NodeOwner, gate *CrossScopeGate) (stepResult, error) {
	var out stepResult
	for _, to := range adjacency[from] {
		if visited[to] {
			continue
		}
		if ownerRef, ok := owner(to); ok && ownerRef != own {
			permitted, err := gate.Permits(ctx, own, ownerRef)
			if err != nil {
				return stepResult{}, err
			}
			if !permitted {
				out.refusals = append(out.refusals, Refusal{NodeID: to, Owner: ownerRef})
				continue
			}
		}
		visited[to] = true
		out.reached = append(out.reached, to)
	}
	return out, nil
}

// buildAdjacency indexes graph's edges by source node id.
func buildAdjacency(g *SymbolGraph) map[string][]string {
	adj := make(map[string][]string, len(g.Nodes))
	for _, e := range g.Edges {
		adj[e.From] = append(adj[e.From], e.To)
	}
	return adj
}
