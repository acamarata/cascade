package docket

import (
	"errors"
	"testing"
)

func TestFirstAttemptOpensClean(t *testing.T) {
	cases := []struct {
		name   string
		ticket *AttemptTicket
	}{
		{"nil attempts slice", &AttemptTicket{Attempts: nil}},
		{"empty attempts slice", &AttemptTicket{Attempts: []Attempt{}}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			newAtt := Attempt{N: 1}
			err := OpenAttempt(tc.ticket, newAtt)
			if err != nil {
				t.Fatalf("expected nil error, got %v", err)
			}
			if len(tc.ticket.Attempts) != 1 {
				t.Fatalf("expected 1 attempt, got %d", len(tc.ticket.Attempts))
			}
			if tc.ticket.Attempts[0].N != 1 {
				t.Errorf("expected N=1, got %d", tc.ticket.Attempts[0].N)
			}
			if tc.ticket.Attempts[0].State != "open" {
				t.Errorf("expected State=open, got %q", tc.ticket.Attempts[0].State)
			}
			if tc.ticket.Attempts[0].Outcome != "" {
				t.Errorf("expected empty Outcome, got %q", tc.ticket.Attempts[0].Outcome)
			}
		})
	}
}

func TestAtMostOneOpenAfterOpen(t *testing.T) {
	t.Run("five successful opens leave exactly one open", func(t *testing.T) {
		ticket := &AttemptTicket{}
		for i := 1; i <= 5; i++ {
			err := OpenAttempt(ticket, Attempt{N: i})
			if err != nil {
				t.Fatalf("unexpected error on open %d: %v", i, err)
			}
		}
		if len(ticket.Attempts) != 5 {
			t.Fatalf("expected 5 attempts, got %d", len(ticket.Attempts))
		}

		openCount := 0
		abandonedCount := 0
		for _, a := range ticket.Attempts {
			if a.State == "open" {
				openCount++
			}
			if a.State == "abandoned" {
				abandonedCount++
			}
		}
		if openCount != 1 {
			t.Errorf("expected exactly 1 open attempt, got %d", openCount)
		}
		if abandonedCount != 4 {
			t.Errorf("expected exactly 4 abandoned attempts, got %d", abandonedCount)
		}
		// The last attempt must be the open one.
		if ticket.Attempts[4].State != "open" {
			t.Errorf("expected last attempt to be open, got %q", ticket.Attempts[4].State)
		}
	})

	t.Run("rejects invalid inputs", func(t *testing.T) {
		invalidCases := []struct {
			name       string
			ticket     *AttemptTicket
			newAttempt Attempt
		}{
			{
				name:       "nil ticket",
				ticket:     nil,
				newAttempt: Attempt{N: 1},
			},
			{
				name: "duplicate existing N",
				ticket: &AttemptTicket{
					Attempts: []Attempt{{N: 1, State: "open"}, {N: 1, State: "abandoned"}},
				},
				newAttempt: Attempt{N: 2},
			},
			{
				name: "nonpositive existing N",
				ticket: &AttemptTicket{
					Attempts: []Attempt{{N: 0, State: "open"}},
				},
				newAttempt: Attempt{N: 2},
			},
			{
				name: "bad absent-outcome State on existing",
				ticket: &AttemptTicket{
					Attempts: []Attempt{{N: 1, State: "invalid"}},
				},
				newAttempt: Attempt{N: 2},
			},
			{
				name: "terminal outcome with empty State on existing",
				ticket: &AttemptTicket{
					Attempts: []Attempt{{N: 1, State: "", Outcome: "success"}},
				},
				newAttempt: Attempt{N: 2},
			},
			{
				name:       "new attempt nonpositive N",
				ticket:     &AttemptTicket{},
				newAttempt: Attempt{N: 0},
			},
			{
				name:       "new attempt non-increasing N",
				ticket:     &AttemptTicket{Attempts: []Attempt{{N: 5, State: "open"}}},
				newAttempt: Attempt{N: 3},
			},
			{
				name:       "new attempt with outcome",
				ticket:     &AttemptTicket{},
				newAttempt: Attempt{N: 1, Outcome: "done"},
			},
			{
				name:       "new attempt with invalid state",
				ticket:     &AttemptTicket{},
				newAttempt: Attempt{N: 1, State: "invalid"},
			},
		}

		for _, tc := range invalidCases {
			t.Run(tc.name, func(t *testing.T) {
				var origAttempts []Attempt
				if tc.ticket != nil {
					origAttempts = make([]Attempt, len(tc.ticket.Attempts))
					copy(origAttempts, tc.ticket.Attempts)
				}

				err := OpenAttempt(tc.ticket, tc.newAttempt)
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if !errors.Is(err, ErrInvalidAttempt) {
					t.Errorf("expected ErrInvalidAttempt, got %v", err)
				}

				if tc.ticket != nil {
					if len(tc.ticket.Attempts) != len(origAttempts) {
						t.Errorf("slice length mutated: expected %d, got %d", len(origAttempts), len(tc.ticket.Attempts))
					}
					for i, a := range origAttempts {
						if tc.ticket.Attempts[i] != a {
							t.Errorf("attempt %d mutated: expected %+v, got %+v", i, a, tc.ticket.Attempts[i])
						}
					}
				}
			})
		}
	})
}
