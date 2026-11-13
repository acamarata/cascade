package docket

import (
	"errors"
	"testing"
)

// TestCloseTicketClosesUntakenInputs verifies that closing a ticket sets all
// its untaken inputs to closed.
func TestCloseTicketClosesUntakenInputs(t *testing.T) {
	s := &CloseInputsStore{
		Inputs: []Input{
			{TicketID: "T1", Status: InputOpen},
			{TicketID: "T1", Status: InputOpen},
		},
	}
	err := CloseTicket(s, "T1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for i, in := range s.Inputs {
		if in.Status != InputClosed {
			t.Errorf("input %d: expected status %q, got %q", i, InputClosed, in.Status)
		}
	}
}

// TestCloseTicketKeepsTakenInputs verifies that taken inputs remain taken
// while untaken inputs are closed.
func TestCloseTicketKeepsTakenInputs(t *testing.T) {
	s := &CloseInputsStore{
		Inputs: []Input{
			{TicketID: "T1", Status: InputTaken},
			{TicketID: "T1", Status: InputOpen},
		},
	}
	err := CloseTicket(s, "T1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.Inputs[0].Status != InputTaken {
		t.Errorf("input 0: expected status %q, got %q", InputTaken, s.Inputs[0].Status)
	}
	if s.Inputs[1].Status != InputClosed {
		t.Errorf("input 1: expected status %q, got %q", InputClosed, s.Inputs[1].Status)
	}
}

// TestCloseTicketLeavesOtherTickets verifies that inputs belonging to other
// tickets are not affected when a ticket is closed.
func TestCloseTicketLeavesOtherTickets(t *testing.T) {
	s := &CloseInputsStore{
		Inputs: []Input{
			{TicketID: "T1", Status: InputOpen},
			{TicketID: "T2", Status: InputOpen},
		},
	}
	err := CloseTicket(s, "T1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.Inputs[0].Status != InputClosed {
		t.Errorf("input 0: expected status %q, got %q", InputClosed, s.Inputs[0].Status)
	}
	if s.Inputs[1].Status != InputOpen {
		t.Errorf("input 1: expected status %q, got %q", InputOpen, s.Inputs[1].Status)
	}
}

// TestCloseTicketUnknownFailsClosed verifies that calling CloseTicket with an
// unknown ticket ID returns ErrTicketUnknown and leaves all inputs unchanged.
func TestCloseTicketUnknownFailsClosed(t *testing.T) {
	s := &CloseInputsStore{
		Inputs: []Input{
			{TicketID: "T1", Status: InputOpen},
		},
	}
	err := CloseTicket(s, "nope")
	if !errors.Is(err, ErrTicketUnknown) {
		t.Fatalf("expected ErrTicketUnknown, got %v", err)
	}
	if s.Inputs[0].Status != InputOpen {
		t.Errorf("input 0: expected status %q, got %q", InputOpen, s.Inputs[0].Status)
	}
}
