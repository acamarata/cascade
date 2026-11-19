package parenttasks

import (
	"errors"
	"testing"
)

func TestAssumeParentsNoParents(t *testing.T) {
	t.Run("nil parents", func(t *testing.T) {
		ticket := Ticket{ID: "t1", Parents: nil, AssumedParents: []string{"ap1"}}
		got, err := AssumeParents(ticket, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.ID != "t1" {
			t.Errorf("ID = %q, want %q", got.ID, "t1")
		}
		if got.Parents != nil {
			t.Errorf("Parents = %v, want nil", got.Parents)
		}
		if len(got.AssumedParents) != 1 || got.AssumedParents[0] != "ap1" {
			t.Errorf("AssumedParents = %v, want [ap1]", got.AssumedParents)
		}
	})

	t.Run("empty parents", func(t *testing.T) {
		ticket := Ticket{ID: "t1", Parents: []string{}, AssumedParents: []string{"ap1"}}
		got, err := AssumeParents(ticket, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.ID != "t1" {
			t.Errorf("ID = %q, want %q", got.ID, "t1")
		}
		if got.Parents == nil {
			t.Errorf("Parents = nil, want empty slice")
		}
		if len(got.Parents) != 0 {
			t.Errorf("Parents = %v, want empty slice", got.Parents)
		}
		if len(got.AssumedParents) != 1 || got.AssumedParents[0] != "ap1" {
			t.Errorf("AssumedParents = %v, want [ap1]", got.AssumedParents)
		}
	})
}

func TestAssumeParentsInvalidInput(t *testing.T) {
	tests := []struct {
		name   string
		ticket Ticket
	}{
		{
			name:   "empty ticket ID",
			ticket: Ticket{ID: "", Parents: []string{"p1"}},
		},
		{
			name:   "empty parent ID",
			ticket: Ticket{ID: "t1", Parents: []string{""}},
		},
		{
			name:   "duplicate parents",
			ticket: Ticket{ID: "t1", Parents: []string{"p1", "p1"}},
		},
		{
			name:   "duplicate assumed parents",
			ticket: Ticket{ID: "t1", Parents: []string{"p1"}, AssumedParents: []string{"ap1", "ap1"}},
		},
		{
			name:   "empty assumed parent",
			ticket: Ticket{ID: "t1", Parents: []string{"p1"}, AssumedParents: []string{""}},
		},
		{
			name:   "malformed assumed parents not subsequence",
			ticket: Ticket{ID: "t1", Parents: []string{"p1", "p2"}, AssumedParents: []string{"p2", "p1"}},
		},
		{
			name:   "assumed parent not in parents",
			ticket: Ticket{ID: "t1", Parents: []string{"p1"}, AssumedParents: []string{"ap1"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Save original to check for mutation
			origParents := append([]string(nil), tt.ticket.Parents...)
			origAssumed := append([]string(nil), tt.ticket.AssumedParents...)

			got, err := AssumeParents(tt.ticket, map[string]bool{"fake": false})
			if !errors.Is(err, ErrInvalidParentInput) {
				t.Errorf("error = %v, want ErrInvalidParentInput", err)
			}
			if got.ID != "" || got.Parents != nil || got.AssumedParents != nil {
				t.Errorf("got = %v, want empty Ticket{}", got)
			}

			// Check no mutation
			if len(tt.ticket.Parents) != len(origParents) {
				t.Errorf("Parents mutated")
			}
			if len(tt.ticket.AssumedParents) != len(origAssumed) {
				t.Errorf("AssumedParents mutated")
			}
		})
	}
}
