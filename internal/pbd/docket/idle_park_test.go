package docket

import "testing"

func TestIdleParkAfterTwoZeroPasses(t *testing.T) {
	ticket := TicketIdle{State: IdleUnparked}

	RecordIdlePass(&ticket, false)
	RecordIdlePass(&ticket, false)

	if ticket.State != IdleParked {
		t.Fatalf("State = %q, want %q", ticket.State, IdleParked)
	}
	if ticket.ZeroPasses != 2 {
		t.Fatalf("ZeroPasses = %d, want 2", ticket.ZeroPasses)
	}
}

func TestIdleParkOneZeroPassStaysUnparked(t *testing.T) {
	ticket := TicketIdle{State: IdleUnparked}

	RecordIdlePass(&ticket, false)

	if ticket.State != IdleUnparked {
		t.Fatalf("State = %q, want %q", ticket.State, IdleUnparked)
	}
	if ticket.ZeroPasses != 1 {
		t.Fatalf("ZeroPasses = %d, want 1", ticket.ZeroPasses)
	}
}

func TestIdleParkProgressResetsCounter(t *testing.T) {
	ticket := TicketIdle{State: IdleUnparked}

	RecordIdlePass(&ticket, false)
	RecordIdlePass(&ticket, true)

	if ticket.ZeroPasses != 0 {
		t.Fatalf("ZeroPasses = %d, want 0", ticket.ZeroPasses)
	}
	if ticket.State != IdleUnparked {
		t.Fatalf("State = %q, want %q", ticket.State, IdleUnparked)
	}
}

func TestIdleParkNonConsecutiveNotParked(t *testing.T) {
	ticket := TicketIdle{State: IdleUnparked}

	RecordIdlePass(&ticket, false)
	RecordIdlePass(&ticket, true)
	RecordIdlePass(&ticket, false)

	if ticket.State != IdleUnparked {
		t.Fatalf("State = %q, want %q", ticket.State, IdleUnparked)
	}
	if ticket.ZeroPasses != 1 {
		t.Fatalf("ZeroPasses = %d, want 1", ticket.ZeroPasses)
	}
}
