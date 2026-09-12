package jobs

import (
	"errors"
	"testing"

	"github.com/acamarata/cascade/internal/conductor"
)

// TestCompileTicket_ProducesTicketInputVerbatimShape asserts
// CompileTicket's PlanInput wraps jobs.TicketInput{ID, ModelClass,
// DependsOn, Footprint} -- no new PlanInput variant, footprint the
// ADD+CHANGE+DELETE union in order.
func TestCompileTicket_ProducesTicketInputVerbatimShape(t *testing.T) {
	c := validContract()
	plan, gates, err := CompileTicket(c, nil)
	if err != nil {
		t.Fatalf("CompileTicket: %v", err)
	}
	if plan.Intent != nil {
		t.Fatal("CompileTicket set PlanInput.Intent; want only Ticket set")
	}
	if plan.Ticket == nil {
		t.Fatal("CompileTicket left PlanInput.Ticket nil")
	}
	if plan.Ticket.ID != c.ID {
		t.Errorf("Ticket.ID = %q, want %q", plan.Ticket.ID, c.ID)
	}
	if plan.Ticket.ModelClass != c.ModelClass {
		t.Errorf("Ticket.ModelClass = %q, want %q", plan.Ticket.ModelClass, c.ModelClass)
	}
	if len(plan.Ticket.DependsOn) != 1 || plan.Ticket.DependsOn[0] != "P1-TEST-T0" {
		t.Errorf("Ticket.DependsOn = %v, want [P1-TEST-T0]", plan.Ticket.DependsOn)
	}
	if len(plan.Ticket.Footprint) != 1 || plan.Ticket.Footprint[0] != "internal/jobs/example.go" {
		t.Errorf("Ticket.Footprint = %v, want [internal/jobs/example.go]", plan.Ticket.Footprint)
	}
	if len(gates) == 0 {
		t.Error("CompileTicket returned an empty declared gate set for a valid Normal-class ticket")
	}
}

// TestCompileTicket_EveryModelClass asserts CompileTicket accepts every
// closed conductor.ModelClass member and refuses everything else.
func TestCompileTicket_EveryModelClass(t *testing.T) {
	valid := []conductor.ModelClass{
		conductor.ModelClassMech, conductor.ModelClassBuild, conductor.ModelClassHeavy,
		conductor.ModelClassReview, conductor.ModelClassArbiter,
	}
	for _, mc := range valid {
		c := validContract()
		c.ModelClass = mc
		if _, _, err := CompileTicket(c, nil); err != nil {
			t.Errorf("CompileTicket(ModelClass=%q): %v", mc, err)
		}
	}
	c := validContract()
	c.ModelClass = "not-a-class"
	_, _, err := CompileTicket(c, nil)
	var unknownClass *ErrUnknownModelClass
	if !errors.As(err, &unknownClass) {
		t.Errorf("CompileTicket(bad ModelClass) error = %v (%T), want *ErrUnknownModelClass", err, err)
	}
}

// TestCompileTicket_EveryCRQACombination covers every R-16.42
// cr_level x qa_level combination and its derived declared gate set.
func TestCompileTicket_EveryCRQACombination(t *testing.T) {
	crForms := []string{"CR-B", "CR-A+CR-B", "CR-B+CR-C", "CR-A+CR-B+CR-C"}
	qaForms := []string{"QA-A", "QA-B", "QA-C"}
	for _, cr := range crForms {
		for _, qa := range qaForms {
			c := validContract()
			c.CRLevel, c.QALevel = cr, qa
			_, gates, err := CompileTicket(c, nil)
			if err != nil {
				t.Fatalf("CompileTicket(cr=%q, qa=%q): %v", cr, qa, err)
			}
			wantClass, _ := declaredRiskClass(cr, qa)
			want, _ := GateSetForRiskClass(wantClass)
			if !gateSetEqual(gates, want) {
				t.Errorf("CompileTicket(cr=%q, qa=%q) gates = %v, want %v (class %q)", cr, qa, gates, want, wantClass)
			}
		}
	}
}

// TestCompileTicket_FootprintUnionOrdering asserts the returned
// TicketInput.Footprint follows ADD, CHANGE, DELETE order.
func TestCompileTicket_FootprintUnionOrdering(t *testing.T) {
	c := validContract()
	c.FilesScopeAdd = []string{"a.go"}
	c.FilesScopeChange = []string{"b.go"}
	c.FilesScopeDelete = []string{"c.go"}
	plan, _, err := CompileTicket(c, nil)
	if err != nil {
		t.Fatalf("CompileTicket: %v", err)
	}
	want := []string{"a.go", "b.go", "c.go"}
	if len(plan.Ticket.Footprint) != len(want) {
		t.Fatalf("Footprint = %v, want %v", plan.Ticket.Footprint, want)
	}
	for i := range want {
		if plan.Ticket.Footprint[i] != want[i] {
			t.Errorf("Footprint[%d] = %q, want %q", i, plan.Ticket.Footprint[i], want[i])
		}
	}
}

// TestCompileTicket_MissingOrUnparseableFieldRefuses spot-checks that a
// malformed contract never yields a partial PlanInput: the zero value
// is returned alongside the typed error.
func TestCompileTicket_MissingOrUnparseableFieldRefuses(t *testing.T) {
	c := validContract()
	c.Title = ""
	plan, gates, err := CompileTicket(c, nil)
	if err == nil {
		t.Fatal("CompileTicket(missing title): want error, got nil")
	}
	var missing *ErrMissingContractField
	if !errors.As(err, &missing) || missing.Field != "title" {
		t.Errorf("CompileTicket(missing title) error = %v, want ErrMissingContractField{title}", err)
	}
	if plan.Ticket != nil || plan.Intent != nil || gates != nil {
		t.Errorf("CompileTicket returned a non-zero PlanInput/GateSet alongside an error: plan=%+v gates=%v", plan, gates)
	}
}

// TestCompileTicket_EmptyIDRefuses asserts the empty-id case yields
// ErrEmptyTicketID specifically, ahead of every other field check.
func TestCompileTicket_EmptyIDRefuses(t *testing.T) {
	c := validContract()
	c.ID = ""
	_, _, err := CompileTicket(c, nil)
	if !errors.Is(err, ErrEmptyTicketID) {
		t.Errorf("CompileTicket(empty id) error = %v, want ErrEmptyTicketID", err)
	}
}
