package jobs

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/pkg/cascade"
)

func generalScope() scope.SessionScope {
	return scope.SessionScope{Kind: scope.ScopeKindGeneral}
}

func TestPlanner_Plan_Ticket_OneNodePerTicket(t *testing.T) {
	pl := NewPlanner(nil)
	dag, err := pl.Plan(context.Background(), PlanInput{Ticket: &TicketInput{
		ID:         "T1",
		ModelClass: conductor.ModelClassBuild,
		DependsOn:  []string{"T0"},
		Footprint:  []string{"docs/README.md"},
		PassThroughFields: PassThroughFields{
			Capabilities: []string{"code"},
			Priority:     2,
		},
	}}, generalScope())
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if len(dag.Nodes) != 1 {
		t.Fatalf("len(dag.Nodes) = %d, want 1", len(dag.Nodes))
	}
	n := dag.Nodes[0]
	if n.ID != "T1" || len(n.Deps) != 1 || n.Deps[0] != "T0" {
		t.Fatalf("node derivation wrong: %+v", n)
	}
	if n.MinTaskClass != conductor.TaskClassCode {
		t.Fatalf("MinTaskClass = %v, want code", n.MinTaskClass)
	}
	if n.RiskClass != RiskClassLow {
		t.Fatalf("RiskClass = %v, want low (docs-only)", n.RiskClass)
	}
	if len(n.Capabilities) != 1 || n.Capabilities[0] != "code" || n.Priority != 2 {
		t.Fatalf("pass-through fields not carried verbatim: %+v", n)
	}
}

func TestPlanner_Plan_Intent_RootNode(t *testing.T) {
	pl := NewPlanner(nil)
	dag, err := pl.Plan(context.Background(), PlanInput{Intent: &IntentInput{Intent: "investigate flaky CI"}}, generalScope())
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	n := dag.Nodes[0]
	if len(n.Deps) != 0 || len(n.MutableScope) != 0 {
		t.Fatalf("intent node must have empty deps/mutable_scope: %+v", n)
	}
	if n.RiskClass != RiskClassNormal {
		t.Fatalf("RiskClass = %v, want normal for an empty footprint", n.RiskClass)
	}
}

func TestPlanner_Plan_UnknownModelClass_TypedError(t *testing.T) {
	pl := NewPlanner(nil)
	_, err := pl.Plan(context.Background(), PlanInput{Ticket: &TicketInput{
		ID:         "T1",
		ModelClass: conductor.ModelClass("bogus"),
	}}, generalScope())
	if err == nil {
		t.Fatal("Plan() = nil error, want typed error for unknown model_class")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
		t.Fatalf("KindOf(err) = %v, %v, want KindInvalidInput", kind, ok)
	}
}

func TestPlanner_Plan_EmptyInput_TypedError(t *testing.T) {
	pl := NewPlanner(nil)
	_, err := pl.Plan(context.Background(), PlanInput{}, generalScope())
	if err == nil {
		t.Fatal("Plan() = nil error, want typed error for empty input")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
		t.Fatalf("KindOf(err) = %v, %v, want KindInvalidInput", kind, ok)
	}
}

func TestPlanner_Plan_BothVariants_TypedError(t *testing.T) {
	pl := NewPlanner(nil)
	_, err := pl.Plan(context.Background(), PlanInput{
		Ticket: &TicketInput{ID: "T1", ModelClass: conductor.ModelClassBuild},
		Intent: &IntentInput{Intent: "x"},
	}, generalScope())
	if err == nil {
		t.Fatal("Plan() = nil error, want typed error when both variants are set")
	}
}

// TestPlanner_Plan_SelfDependencyCycle is the ticket-mandated real
// cyclic input at the Planner.Plan boundary: a ticket declaring itself
// as its own dependency is a self-loop cycle.
func TestPlanner_Plan_SelfDependencyCycle(t *testing.T) {
	pl := NewPlanner(nil)
	_, err := pl.Plan(context.Background(), PlanInput{Ticket: &TicketInput{
		ID:         "T1",
		ModelClass: conductor.ModelClassBuild,
		DependsOn:  []string{"T1"},
	}}, generalScope())
	if err == nil {
		t.Fatal("Plan() = nil error, want cycle error for self-dependency")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
		t.Fatalf("KindOf(err) = %v, %v, want KindInvalidInput", kind, ok)
	}
}

func TestPlanner_Plan_ModelClassMapping(t *testing.T) {
	cases := []struct {
		mc   conductor.ModelClass
		want conductor.TaskClass
	}{
		{conductor.ModelClassMech, conductor.TaskClassCode},
		{conductor.ModelClassBuild, conductor.TaskClassCode},
		{conductor.ModelClassHeavy, conductor.TaskClassCode},
		{conductor.ModelClassReview, conductor.TaskClassReview},
		{conductor.ModelClassArbiter, conductor.TaskClassArbitrate},
	}
	pl := NewPlanner(nil)
	for _, c := range cases {
		dag, err := pl.Plan(context.Background(), PlanInput{Ticket: &TicketInput{ID: "T1", ModelClass: c.mc}}, generalScope())
		if err != nil {
			t.Fatalf("Plan(%v) error = %v", c.mc, err)
		}
		if dag.Nodes[0].MinTaskClass != c.want {
			t.Fatalf("Plan(%v) MinTaskClass = %v, want %v", c.mc, dag.Nodes[0].MinTaskClass, c.want)
		}
	}
}

func TestPlanner_Plan_ReachabilitySeamErrorFailsClosedToCritical(t *testing.T) {
	failing := func(_ context.Context, _ []string) ([]string, error) {
		return nil, cascade.New(cascade.KindUnavailable, "reachability graph unavailable")
	}
	pl := NewPlanner(failing)
	dag, err := pl.Plan(context.Background(), PlanInput{Ticket: &TicketInput{
		ID:         "T1",
		ModelClass: conductor.ModelClassBuild,
		Footprint:  []string{"docs/README.md"},
	}}, generalScope())
	if err != nil {
		t.Fatalf("Plan() error = %v, want Plan to succeed with Critical class on seam failure", err)
	}
	if dag.Nodes[0].RiskClass != RiskClassCritical {
		t.Fatalf("RiskClass = %v, want critical (seam error fails closed)", dag.Nodes[0].RiskClass)
	}
}
