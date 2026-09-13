package economics

import "testing"

func TestParseReservationState(t *testing.T) {
	cases := []struct {
		in      string
		want    ReservationState
		wantErr bool
	}{
		{"held", ReservationHeld, false},
		{"parked", ReservationParked, false},
		{"committed", ReservationCommitted, false},
		{"released", ReservationReleased, false},
		{"rolled_back", ReservationRolledBack, false},
		{"", "", true},
		{"bogus", "", true},
	}
	for _, c := range cases {
		got, err := ParseReservationState(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParseReservationState(%q) = nil error, want ErrUnknownReservationState", c.in)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("ParseReservationState(%q) = %v, %v; want %v, nil", c.in, got, err, c.want)
		}
	}
}

func TestParseReservationKind(t *testing.T) {
	cases := []struct {
		in      string
		want    ReservationKind
		wantErr bool
	}{
		{"interactive", ReservationInteractive, false},
		{"batch", ReservationBatch, false},
		{"", "", true},
		{"triage", "", true},
	}
	for _, c := range cases {
		got, err := ParseReservationKind(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParseReservationKind(%q) = nil error, want ErrUnknownReservationKind", c.in)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("ParseReservationKind(%q) = %v, %v; want %v, nil", c.in, got, err, c.want)
		}
	}
}

// TestReservationStateTransitionMatrix covers the FULL closed transition
// table: every legal row plus enough illegal rows to prove no move
// outside the table is ever accepted.
func TestReservationStateTransitionMatrix(t *testing.T) {
	all := []ReservationState{ReservationHeld, ReservationParked, ReservationCommitted, ReservationReleased, ReservationRolledBack}
	legal := map[[2]ReservationState]bool{
		{ReservationHeld, ReservationParked}:        true,
		{ReservationParked, ReservationHeld}:        true,
		{ReservationHeld, ReservationCommitted}:     true,
		{ReservationCommitted, ReservationParked}:   true,
		{ReservationParked, ReservationCommitted}:   false, // NOT legal -- only held->committed
		{ReservationHeld, ReservationReleased}:      true,
		{ReservationHeld, ReservationRolledBack}:    true,
		{ReservationParked, ReservationReleased}:    true,
		{ReservationParked, ReservationRolledBack}:  true,
		{ReservationCommitted, ReservationReleased}: true,
	}
	for _, from := range all {
		for _, to := range all {
			want, explicit := legal[[2]ReservationState{from, to}]
			if !explicit {
				want = false
			}
			got := TransitionAllowed(from, to)
			if got != want {
				t.Errorf("TransitionAllowed(%s, %s) = %v, want %v", from, to, got, want)
			}
		}
	}
}

func TestReservationStateTerminal(t *testing.T) {
	if !ReservationReleased.Terminal() {
		t.Error("released must be terminal")
	}
	if !ReservationRolledBack.Terminal() {
		t.Error("rolled_back must be terminal")
	}
	if ReservationHeld.Terminal() || ReservationParked.Terminal() || ReservationCommitted.Terminal() {
		t.Error("held/parked/committed must not be terminal")
	}
}
