package docket

import (
	"errors"
	"reflect"
	"testing"
)

// TestRefusedMarkSurvivesReplan verifies that refused marks survive replan operations,
// returned slices/maps do not alias inputs, and invalid inputs return ErrInvalidSnapshot.
func TestRefusedMarkSurvivesReplan(t *testing.T) {
	// Create a snapshot with T1 marked as refused (true)
	snapshot := Snapshot{
		TicketIDs: []string{"T1", "T2"},
		RefusedMarks: map[string]bool{
			"T1": true,
			"T2": false,
		},
	}

	// Create a plan
	plan := Plan{
		TicketIDs: []string{"T1", "T2", "T3"},
	}

	// Call Replan
	result, err := Replan(snapshot, plan)
	if err != nil {
		t.Fatalf("Replan failed: %v", err)
	}

	// Verify T1's refused mark survived
	if !result.RefusedMarks["T1"] {
		t.Error("T1's refused mark did not survive Replan")
	}

	// Verify returned slices do not alias inputs
	if len(result.TicketIDs) > 0 && len(snapshot.TicketIDs) > 0 {
		if &result.TicketIDs[0] == &snapshot.TicketIDs[0] {
			t.Error("result.TicketIDs aliases snapshot.TicketIDs")
		}
	}
	if len(result.TicketIDs) > 0 && len(plan.TicketIDs) > 0 {
		if &result.TicketIDs[0] == &plan.TicketIDs[0] {
			t.Error("result.TicketIDs aliases plan.TicketIDs")
		}
	}

	// Verify returned maps do not alias inputs
	if reflect.ValueOf(result.RefusedMarks).Pointer() == reflect.ValueOf(snapshot.RefusedMarks).Pointer() {
		t.Error("result.RefusedMarks aliases snapshot.RefusedMarks")
	}

	// Test invalid/duplicate/empty IDs in snapshot
	invalidSnapshot := Snapshot{
		TicketIDs:    []string{"T1", "T1"}, // duplicate
		RefusedMarks: map[string]bool{"T1": true},
	}
	_, err = Replan(invalidSnapshot, plan)
	if !errors.Is(err, ErrInvalidSnapshot) {
		t.Errorf("Expected ErrInvalidSnapshot for duplicate IDs, got %v", err)
	}

	// Test empty IDs in plan
	emptyPlan := Plan{TicketIDs: []string{}}
	_, err = Replan(invalidSnapshot, emptyPlan)
	if !errors.Is(err, ErrInvalidSnapshot) {
		t.Errorf("Expected ErrInvalidSnapshot for invalid snapshot, got %v", err)
	}
}

// TestReplanDroppedTicketKeepsMark verifies that dropped tickets retain their marks,
// nil/empty plan empties TicketIDs while preserving marks, and nil/empty snapshot succeeds.
func TestReplanDroppedTicketKeepsMark(t *testing.T) {
	// Create a snapshot with T1 marked as refused
	snapshot := Snapshot{
		TicketIDs: []string{"T1", "T2"},
		RefusedMarks: map[string]bool{
			"T1": true,
		},
	}

	// Create a plan that drops T1 (only includes T2)
	plan := Plan{
		TicketIDs: []string{"T2"},
	}

	// Call Replan
	result, err := Replan(snapshot, plan)
	if err != nil {
		t.Fatalf("Replan failed: %v", err)
	}

	// Verify dropped T1 retains its mark
	if !result.RefusedMarks["T1"] {
		t.Error("Dropped T1 did not retain its refused mark")
	}

	// Test nil/empty plan empties TicketIDs and preserves marks
	emptyPlan := Plan{}
	result2, err := Replan(snapshot, emptyPlan)
	if err != nil {
		t.Fatalf("Replan with empty plan failed: %v", err)
	}
	if len(result2.TicketIDs) != 0 {
		t.Errorf("Expected empty TicketIDs with empty plan, got %v", result2.TicketIDs)
	}
	// Verify marks are preserved
	if !result2.RefusedMarks["T1"] {
		t.Error("T1's mark was not preserved with empty plan")
	}

	// Test nil/empty snapshot succeeds
	emptySnapshot := Snapshot{}
	result3, err := Replan(emptySnapshot, plan)
	if err != nil {
		t.Errorf("Replan with empty snapshot failed: %v", err)
	}
	_ = result3
}
