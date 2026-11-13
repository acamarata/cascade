package docket

import "testing"

func TestOlderOpenAttemptAbandoned(t *testing.T) {
	ticket := &AttemptTicket{
		Attempts: []Attempt{
			{N: 1, State: "open", Outcome: ""},
			{N: 2, State: "open", Outcome: ""},
		},
	}
	err := OpenAttempt(ticket, Attempt{N: 3})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ticket.Attempts) != 3 {
		t.Fatalf("expected 3 attempts, got %d", len(ticket.Attempts))
	}
	if ticket.Attempts[0].State != "abandoned" || ticket.Attempts[0].Outcome != "" {
		t.Errorf("attempt 1 not abandoned: %+v", ticket.Attempts[0])
	}
	if ticket.Attempts[1].State != "abandoned" || ticket.Attempts[1].Outcome != "" {
		t.Errorf("attempt 2 not abandoned: %+v", ticket.Attempts[1])
	}
	if ticket.Attempts[2].N != 3 || ticket.Attempts[2].State != "open" || ticket.Attempts[2].Outcome != "" {
		t.Errorf("attempt 3 incorrect: %+v", ticket.Attempts[2])
	}
}

func TestOutcomeAttemptsUntouched(t *testing.T) {
	ticket := &AttemptTicket{
		Attempts: []Attempt{
			{N: 1, State: "arbitrary", Outcome: "pass"},
		},
	}
	err := OpenAttempt(ticket, Attempt{N: 2})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ticket.Attempts[0].State != "arbitrary" || ticket.Attempts[0].Outcome != "pass" {
		t.Errorf("terminal attempt modified: %+v", ticket.Attempts[0])
	}
}

func TestAbandonedIsIdempotent(t *testing.T) {
	ticket := &AttemptTicket{
		Attempts: []Attempt{
			{N: 1, State: "abandoned", Outcome: ""},
		},
	}
	err := OpenAttempt(ticket, Attempt{N: 2})
	if err != nil {
		t.Fatalf("unexpected error on first open: %v", err)
	}
	if ticket.Attempts[0].State != "abandoned" {
		t.Errorf("abandoned attempt modified: %+v", ticket.Attempts[0])
	}
	if ticket.Attempts[1].State != "open" {
		t.Errorf("new attempt not open: %+v", ticket.Attempts[1])
	}

	// Successive valid opens keep it unchanged and abandon only the prior open attempt
	err = OpenAttempt(ticket, Attempt{N: 3})
	if err != nil {
		t.Fatalf("unexpected error on second open: %v", err)
	}
	if ticket.Attempts[0].State != "abandoned" {
		t.Errorf("abandoned attempt modified on second open: %+v", ticket.Attempts[0])
	}
	if ticket.Attempts[1].State != "abandoned" {
		t.Errorf("prior open not abandoned on second open: %+v", ticket.Attempts[1])
	}
	if ticket.Attempts[2].State != "open" {
		t.Errorf("new attempt not open: %+v", ticket.Attempts[2])
	}
}
