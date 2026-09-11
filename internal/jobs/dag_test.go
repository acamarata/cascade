package jobs

import (
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestDagValidateAcyclic_NoCycle(t *testing.T) {
	nodes := []DagNode{
		{ID: "a", Deps: []string{"b"}},
		{ID: "b", Deps: []string{"c"}},
		{ID: "c"},
	}
	if err := validateAcyclic(nodes); err != nil {
		t.Fatalf("validateAcyclic() = %v, want nil", err)
	}
}

// TestDagValidateAcyclic_RealCycle is the ticket-mandated real cyclic
// input: a -> b -> c -> a.
func TestDagValidateAcyclic_RealCycle(t *testing.T) {
	nodes := []DagNode{
		{ID: "a", Deps: []string{"b"}},
		{ID: "b", Deps: []string{"c"}},
		{ID: "c", Deps: []string{"a"}},
	}
	err := validateAcyclic(nodes)
	if err == nil {
		t.Fatal("validateAcyclic() = nil, want cycle error")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
		t.Fatalf("KindOf(err) = %v, %v, want KindInvalidInput", kind, ok)
	}
}

func TestDagValidateAcyclic_SelfLoop(t *testing.T) {
	nodes := []DagNode{{ID: "a", Deps: []string{"a"}}}
	if err := validateAcyclic(nodes); err == nil {
		t.Fatal("validateAcyclic() = nil, want self-loop cycle error")
	}
}

func TestDagValidateAcyclic_DepOutsideSet(t *testing.T) {
	// A dep referencing an id not present in the node set is not this
	// function's concern (planner.go resolves depends_on before this
	// runs) -- it must not be mistaken for a cycle.
	nodes := []DagNode{{ID: "a", Deps: []string{"ghost"}}}
	if err := validateAcyclic(nodes); err != nil {
		t.Fatalf("validateAcyclic() = %v, want nil for an out-of-set dep", err)
	}
}

func TestExecutionDag_NodeByID(t *testing.T) {
	dag := ExecutionDag{Nodes: []DagNode{{ID: "a"}, {ID: "b"}}}
	if _, ok := dag.NodeByID("a"); !ok {
		t.Fatal("NodeByID(a) not found")
	}
	if _, ok := dag.NodeByID("missing"); ok {
		t.Fatal("NodeByID(missing) unexpectedly found")
	}
}
