package docket

import (
	"errors"
	"testing"
)

func TestResetFirstAllowed(t *testing.T) {
	if err := CheckReset(StateFailed, 0); err != nil {
		t.Fatalf("CheckReset(StateFailed, 0) = %v, want nil", err)
	}

	cases := []struct {
		name   string
		state  TicketState
		resets int
	}{
		{name: "negative resets", state: StateFailed, resets: -1},
		{name: "empty state", state: TicketState(""), resets: 0},
		{name: "unknown state", state: TicketState("unknown"), resets: 1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckReset(tc.state, tc.resets)
			if !errors.Is(err, ErrResetInvalidInput) {
				t.Fatalf("CheckReset(%q, %d) = %v, want %v", tc.state, tc.resets, err, ErrResetInvalidInput)
			}
		})
	}
}

func TestResetSecondAllowed(t *testing.T) {
	if err := CheckReset(StateFailed, 1); err != nil {
		t.Fatalf("CheckReset(StateFailed, 1) = %v, want nil", err)
	}
}

func TestResetThirdRefused(t *testing.T) {
	for _, resets := range []int{2, 3, 10} {
		err := CheckReset(StateFailed, resets)
		if !errors.Is(err, ErrResetCapExceeded) {
			t.Fatalf("CheckReset(StateFailed, %d) = %v, want %v", resets, err, ErrResetCapExceeded)
		}
		if got, want := err.Error(), "reset: cap exceeded"; got != want {
			t.Fatalf("CheckReset(StateFailed, %d).Error() = %q, want %q", resets, got, want)
		}
	}
}
