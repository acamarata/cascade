package jobs

import (
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/conductor"
)

func TestTicketInput_PassThroughFieldsCarried(t *testing.T) {
	ti := TicketInput{
		ID:         "T1",
		ModelClass: conductor.ModelClassBuild,
		DependsOn:  []string{"T0"},
		Footprint:  []string{"internal/jobs/dag.go"},
		PassThroughFields: PassThroughFields{
			Capabilities:     []string{"code"},
			NodeRequirements: map[string]string{"os": "linux"},
			Timeout:          30 * time.Second,
			CostCeiling:      1.5,
			Priority:         3,
		},
	}
	if len(ti.Capabilities) != 1 || ti.Capabilities[0] != "code" {
		t.Fatalf("Capabilities not carried verbatim: %v", ti.Capabilities)
	}
	if ti.Timeout != 30*time.Second || ti.CostCeiling != 1.5 || ti.Priority != 3 {
		t.Fatalf("pass-through fields not carried verbatim: %+v", ti)
	}
}

func TestIntentInput_PassThroughFieldsCarried(t *testing.T) {
	in := IntentInput{
		Intent: "fix the flaky test",
		PassThroughFields: PassThroughFields{
			Priority: 7,
		},
	}
	if in.Priority != 7 {
		t.Fatalf("Priority not carried verbatim: %d", in.Priority)
	}
}

func TestPlanInput_ExactlyOneVariant(t *testing.T) {
	empty := PlanInput{}
	if empty.Ticket != nil || empty.Intent != nil {
		t.Fatal("zero-value PlanInput must carry neither variant")
	}
}
