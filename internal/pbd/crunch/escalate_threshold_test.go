package crunch

import (
	"errors"
	"testing"
)

// TestEscalateAfter3GCFailures verifies escalation triggers after 3 or more GC failures.
func TestEscalateAfter3GCFailures(t *testing.T) {
	tests := []struct {
		name string
		gc   int
	}{
		{"exactly 3", 3},
		{"more than 3", 5},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ticket := EscalationTicket{
				ID:         "req-1",
				Lane:       "junior",
				GCFailures: tc.gc,
				Attempts:   0,
			}
			res, err := Escalate(ticket)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !res.Escalated {
				t.Errorf("expected Escalated=true, got false")
			}
			if res.Lane != "senior" {
				t.Errorf("expected Lane=senior, got %q", res.Lane)
			}
			if !res.AllowRetry {
				t.Errorf("expected AllowRetry=true for Attempts=0, got false")
			}
		})
	}
}

// TestEscalateAfter2ModelFailures verifies escalation triggers after 2 or more model failures.
func TestEscalateAfter2ModelFailures(t *testing.T) {
	tests := []struct {
		name string
		mf   int
	}{
		{"exactly 2", 2},
		{"more than 2", 4},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ticket := EscalationTicket{
				ID:            "req-2",
				Lane:          "junior",
				ModelFailures: tc.mf,
				Attempts:      0,
			}
			res, err := Escalate(ticket)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !res.Escalated {
				t.Errorf("expected Escalated=true, got false")
			}
			if res.Lane != "senior" {
				t.Errorf("expected Lane=senior, got %q", res.Lane)
			}
			if !res.AllowRetry {
				t.Errorf("expected AllowRetry=true for Attempts=0, got false")
			}
		})
	}
}

// TestSeniorLaneRetryLimitEnforced verifies senior lane retries are limited to 2 attempts.
func TestSeniorLaneRetryLimitEnforced(t *testing.T) {
	cases := []struct {
		name      string
		attempts  int
		gc        int
		mf        int
		wantRetry bool
		wantEsc   bool
		wantErr   error
	}{
		{"senior attempts 0 below threshold", 0, 0, 0, true, false, nil},
		{"senior attempts 1 below threshold", 1, 0, 0, true, false, nil},
		{"senior attempts 2 below threshold", 2, 0, 0, false, false, nil},
		{"senior attempts 0 threshold gc", 0, 3, 0, true, true, nil},
		{"senior attempts 1 threshold gc", 1, 3, 0, true, true, nil},
		{"senior attempts 2 threshold gc", 2, 3, 0, false, true, nil},
		{"senior attempts 2 threshold model", 2, 0, 2, false, true, nil},
		{"senior attempts 3 exhausted", 3, 0, 0, false, false, ErrEscalationExhausted},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ticket := EscalationTicket{
				ID:            "req-3",
				Lane:          "senior",
				GCFailures:    tc.gc,
				ModelFailures: tc.mf,
				Attempts:      tc.attempts,
			}
			res, err := Escalate(ticket)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("expected error %v, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if res.AllowRetry != tc.wantRetry {
				t.Errorf("AllowRetry: want %v, got %v", tc.wantRetry, res.AllowRetry)
			}
			if res.Escalated != tc.wantEsc {
				t.Errorf("Escalated: want %v, got %v", tc.wantEsc, res.Escalated)
			}
			if res.Lane != "senior" {
				t.Errorf("Lane: want senior, got %q", res.Lane)
			}
		})
	}
}
