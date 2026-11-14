package docket

import (
	"testing"
)

// TestReplanNewTicketUnrefused verifies that a new ticket added by Replan
// has no refused mark entry (comma-ok returns false, false), that TicketIDs
// follow the plan order exactly, and that prior false marks remain false.
func TestReplanNewTicketUnrefused(t *testing.T) {
	snap := Snapshot{
		TicketIDs: []string{"T1", "T2"},
		RefusedMarks: map[string]bool{
			"T1": false,
			"T2": true,
		},
	}
	plan := Plan{
		TicketIDs: []string{"T2", "T9"},
	}

	got, err := Replan(snap, plan)
	if err != nil {
		t.Fatalf("Replan returned unexpected error: %v", err)
	}

	// TicketIDs must exactly follow plan order.
	if len(got.TicketIDs) != 2 || got.TicketIDs[0] != "T2" || got.TicketIDs[1] != "T9" {
		t.Fatalf("unexpected TicketIDs: %v", got.TicketIDs)
	}

	// Prior false mark remains false.
	v, ok := got.RefusedMarks["T2"]
	if !ok || v != true {
		t.Fatalf("expected T2 mark to remain true, got %v, %v", v, ok)
	}

	v1, ok1 := got.RefusedMarks["T1"]
	if !ok1 || v1 != false {
		t.Fatalf("expected T1 mark to remain false, got %v, %v", v1, ok1)
	}

	// New T9 has absent map entry; comma-ok returns false, false.
	v9, ok9 := got.RefusedMarks["T9"]
	if ok9 || v9 {
		t.Fatalf("expected T9 to be unrefused (false, false), got %v, %v", v9, ok9)
	}

	// Original snapshot must be untouched.
	if len(snap.TicketIDs) != 2 || snap.TicketIDs[0] != "T1" {
		t.Fatal("original snapshot was modified")
	}
}
