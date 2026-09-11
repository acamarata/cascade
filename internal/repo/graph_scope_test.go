package repo

import (
	"context"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/pkg/cascade"
)

// crossScopeFixtureGraph mirrors the shape reachability_test.go uses: a
// local package that imports a foreign package, which in turn imports
// back into the local package (a real cycle across the boundary).
func crossScopeFixtureGraph() *SymbolGraph {
	return &SymbolGraph{
		Nodes: []GraphNode{
			{ID: "local", Kind: NodePackage, Package: "local", File: "local/main.go"},
			{ID: "local#Run", Kind: NodeFunc, Name: "Run", Package: "local", File: "local/main.go"},
			{ID: "foreign", Kind: NodePackage, Package: "foreign", File: "foreign/foreign.go"},
			{ID: "foreign#Do", Kind: NodeFunc, Name: "Do", Package: "foreign", File: "foreign/foreign.go"},
		},
		Edges: []GraphEdge{
			{From: "local", To: "local#Run", Kind: EdgeDeclares},
			{From: "local#Run", To: "foreign", Kind: EdgeImports},
			{From: "foreign", To: "foreign#Do", Kind: EdgeDeclares},
			{From: "foreign#Do", To: "local", Kind: EdgeCalls},
		},
	}
}

var (
	ownScope    = scope.Ref{Kind: scope.ScopeKindProject, ID: "own"}
	otherScope  = scope.Ref{Kind: scope.ScopeKindProject, ID: "other"}
	foreignNode = map[string]bool{"foreign": true, "foreign#Do": true}
)

func foreignOwner(id string) (scope.Ref, bool) {
	if foreignNode[id] {
		return otherScope, true
	}
	return scope.Ref{}, false
}

func TestGatedWalk_NoPermittingEdge_RefusesAndStops(t *testing.T) {
	db := openTestDB(t)
	gate, err := newCrossScopeGate(scope.NewGraphStore(db))
	if err != nil {
		t.Fatalf("newCrossScopeGate: %v", err)
	}
	result, err := gatedWalk(context.Background(), crossScopeFixtureGraph(), []string{"local"}, ownScope, foreignOwner, gate)
	if err != nil {
		t.Fatalf("gatedWalk: %v", err)
	}
	for _, id := range result.Reached {
		if foreignNode[id] {
			t.Errorf("leak: walk reached foreign node %q with no permitting scope edge", id)
		}
	}
	if len(result.Refusals) != 1 || result.Refusals[0].NodeID != "foreign" || result.Refusals[0].Owner != otherScope {
		t.Errorf("Refusals = %+v, want exactly one refusal at %q owned by %+v", result.Refusals, "foreign", otherScope)
	}
}

// TestGatedWalk_PermittingEdge_IsTheOnlyThingThatOpensIt proves the
// previous test is not passing because the walk reaches nothing
// regardless: adding the one declared depends_on edge, and nothing else,
// opens the foreign side.
func TestGatedWalk_PermittingEdge_IsTheOnlyThingThatOpensIt(t *testing.T) {
	db := openTestDB(t)
	store := scope.NewGraphStore(db)
	ctx := context.Background()
	if err := store.PutEdge(ctx, scope.Edge{From: ownScope, To: otherScope, Kind: scope.EdgeKindDependsOn}); err != nil {
		t.Fatalf("PutEdge: %v", err)
	}
	gate, err := newCrossScopeGate(store)
	if err != nil {
		t.Fatalf("newCrossScopeGate: %v", err)
	}
	result, err := gatedWalk(ctx, crossScopeFixtureGraph(), []string{"local"}, ownScope, foreignOwner, gate)
	if err != nil {
		t.Fatalf("gatedWalk: %v", err)
	}
	if len(result.Refusals) != 0 {
		t.Errorf("Refusals = %+v, want none once a depends_on edge exists", result.Refusals)
	}
	for id := range foreignNode {
		found := false
		for _, r := range result.Reached {
			if r == id {
				found = true
			}
		}
		if !found {
			t.Errorf("Reached = %v, want it to include foreign node %q once permitted", result.Reached, id)
		}
	}
}

// TestGatedWalk_RealCycleAcrossTheBoundaryDoesNotHang proves the
// cross-scope cycle (foreign#Do -> local, once permitted) does not loop
// the walk: local is already visited as a seed, so it is never re-queued.
func TestGatedWalk_RealCycleAcrossTheBoundaryDoesNotHang(t *testing.T) {
	db := openTestDB(t)
	store := scope.NewGraphStore(db)
	ctx := context.Background()
	if err := store.PutEdge(ctx, scope.Edge{From: ownScope, To: otherScope, Kind: scope.EdgeKindSharesContextWith}); err != nil {
		t.Fatalf("PutEdge: %v", err)
	}
	gate, err := newCrossScopeGate(store)
	if err != nil {
		t.Fatalf("newCrossScopeGate: %v", err)
	}
	done := make(chan CrossScopeWalkResult, 1)
	go func() {
		result, _ := gatedWalk(ctx, crossScopeFixtureGraph(), []string{"local"}, ownScope, foreignOwner, gate)
		done <- result
	}()
	select {
	case result := <-done:
		if len(result.Reached) != 4 {
			t.Errorf("Reached = %v, want all 4 nodes exactly once", result.Reached)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("gatedWalk did not terminate on a cross-scope cycle")
	}
}

func TestNewCrossScopeGate_NilStoreIsInvalidInput(t *testing.T) {
	_, err := newCrossScopeGate(nil)
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
		t.Errorf("newCrossScopeGate(nil): kind = %v, ok=%v, want KindInvalidInput", kind, ok)
	}
}

func TestGatedWalk_NilGraphIsInvalidInput(t *testing.T) {
	db := openTestDB(t)
	gate, err := newCrossScopeGate(scope.NewGraphStore(db))
	if err != nil {
		t.Fatalf("newCrossScopeGate: %v", err)
	}
	_, err = gatedWalk(context.Background(), nil, nil, ownScope, foreignOwner, gate)
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
		t.Errorf("gatedWalk(nil graph): kind = %v, ok=%v, want KindInvalidInput", kind, ok)
	}
}
