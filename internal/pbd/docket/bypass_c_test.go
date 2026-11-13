package docket

import "testing"

func TestBypassDeniedWhenGateDeadlinesBelowThree(t *testing.T) {
	// over-cap Hot A3 with deadlines 2 and seats 0
	in := BypassInput{
		Lane:          "A3",
		Hot:           true,
		Cap:           10,
		Occupancy:     10,
		GateDeadlines: 2,
		RunSeats:      0,
	}
	dec, err := Bypass(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dec.Admitted || dec.Bypassed {
		t.Fatalf("expected {false,false}, got %+v", dec)
	}

	// Hot false also denies even at deadlines 3 and zero seats
	in.Hot = false
	in.GateDeadlines = 3
	dec, err = Bypass(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dec.Admitted || dec.Bypassed {
		t.Fatalf("expected {false,false} for hot=false, got %+v", dec)
	}
}

func TestBypassDeniedWhenRunSeatsAvailable(t *testing.T) {
	// over-cap Hot A3 with deadlines 3 and seats 1
	in := BypassInput{
		Lane:          "A3",
		Hot:           true,
		Cap:           10,
		Occupancy:     10,
		GateDeadlines: 3,
		RunSeats:      1,
	}
	dec, err := Bypass(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dec.Admitted || dec.Bypassed {
		t.Fatalf("expected {false,false}, got %+v", dec)
	}
}
