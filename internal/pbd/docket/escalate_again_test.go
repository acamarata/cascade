package docket

import "testing"

func TestEscalateAgainResetsWhenLaneUntried(t *testing.T) {
	ticket := &escalateTicket{ResetCount: 0, TriedLanes: []string{"A"}}
	bigLanes := []string{"A", "B"}
	if !escalateAgain(ticket, bigLanes) {
		t.Fatal("expected true")
	}
	if ticket.ResetCount != 1 {
		t.Fatalf("expected ResetCount 1, got %d", ticket.ResetCount)
	}
}

func TestEscalateAgainStopsAtFour(t *testing.T) {
	ticket := &escalateTicket{ResetCount: 4, TriedLanes: []string{"A"}}
	bigLanes := []string{"A", "B"}
	if escalateAgain(ticket, bigLanes) {
		t.Fatal("expected false")
	}
	if ticket.ResetCount != 4 {
		t.Fatalf("expected ResetCount 4, got %d", ticket.ResetCount)
	}
}

func TestEscalateAgainStopsWhenNoUntriedLane(t *testing.T) {
	ticket := &escalateTicket{ResetCount: 1, TriedLanes: []string{"A"}}
	bigLanes := []string{"A"}
	if escalateAgain(ticket, bigLanes) {
		t.Fatal("expected false")
	}
}

func TestEscalateAgainIncrementsCounter(t *testing.T) {
	ticket := &escalateTicket{ResetCount: 2, TriedLanes: []string{"A"}}
	bigLanes := []string{"A", "B"}
	if !escalateAgain(ticket, bigLanes) {
		t.Fatal("expected true")
	}
	if ticket.ResetCount != 3 {
		t.Fatalf("expected ResetCount 3, got %d", ticket.ResetCount)
	}
}
