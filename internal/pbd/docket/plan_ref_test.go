package docket

import "testing"

// TestPlanCarriesTicketIDs pins the Plan shape a later docket slice consumes.
func TestPlanCarriesTicketIDs(t *testing.T) {
	p := Plan{TicketIDs: []string{"a", "b"}}
	if len(p.TicketIDs) != 2 || p.TicketIDs[1] != "b" {
		t.Fatalf("Plan.TicketIDs = %v", p.TicketIDs)
	}
}
