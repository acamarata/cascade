package docket

import (
	"errors"
	"testing"
)

type bypassCase struct {
	name    string
	input   BypassInput
	want    BypassDecision
	wantErr bool
}

func bypassAdmissionCases() []bypassCase {
	return []bypassCase{
		{
			name:    "A3 below limit admitted",
			input:   BypassInput{Lane: "A3", Hot: true, Cap: 10, OverflowCap: 5, Occupancy: 3, GateDeadlines: 2, RunSeats: 1},
			want:    BypassDecision{Admitted: true, Bypassed: false},
			wantErr: false,
		},
		{
			name:    "A2 below limit admitted",
			input:   BypassInput{Lane: "A2", Hot: false, Cap: 10, OverflowCap: 5, Occupancy: 3, GateDeadlines: 2, RunSeats: 1},
			want:    BypassDecision{Admitted: true, Bypassed: false},
			wantErr: false,
		},
		{
			name:    "zero capacity and Hot false denied",
			input:   BypassInput{Lane: "A3", Hot: false, Cap: 0, OverflowCap: 0, Occupancy: 0, GateDeadlines: 0, RunSeats: 0},
			want:    BypassDecision{Admitted: false, Bypassed: false},
			wantErr: false,
		},
	}
}

func bypassInvalidCases() []bypassCase {
	return []bypassCase{
		{
			name:    "empty lane invalid",
			input:   BypassInput{Lane: "", Cap: 10},
			want:    BypassDecision{},
			wantErr: true,
		},
		{
			name:    "invalid lane value",
			input:   BypassInput{Lane: "A1", Cap: 10},
			want:    BypassDecision{},
			wantErr: true,
		},
		{
			name:    "negative Cap invalid",
			input:   BypassInput{Lane: "A3", Cap: -1},
			want:    BypassDecision{},
			wantErr: true,
		},
		{
			name:    "negative OverflowCap invalid",
			input:   BypassInput{Lane: "A3", OverflowCap: -1},
			want:    BypassDecision{},
			wantErr: true,
		},
		{
			name:    "negative Occupancy invalid",
			input:   BypassInput{Lane: "A3", Occupancy: -1},
			want:    BypassDecision{},
			wantErr: true,
		},
		{
			name:    "negative GateDeadlines invalid",
			input:   BypassInput{Lane: "A3", GateDeadlines: -1},
			want:    BypassDecision{},
			wantErr: true,
		},
		{
			name:    "negative RunSeats invalid",
			input:   BypassInput{Lane: "A3", RunSeats: -1},
			want:    BypassDecision{},
			wantErr: true,
		},
	}
}

// TestBypassDeniedWhenCapNotExceeded verifies admission decisions and input validation.
func TestBypassDeniedWhenCapNotExceeded(t *testing.T) {
	tests := append(bypassAdmissionCases(), bypassInvalidCases()...)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			orig := tt.input
			got, err := Bypass(tt.input)
			if tt.input != orig {
				t.Fatal("input was modified")
			}
			if tt.wantErr {
				if !errors.Is(err, ErrInvalidBypassInput) {
					t.Fatalf("expected ErrInvalidBypassInput, got %v", err)
				}
				if got != (BypassDecision{}) {
					t.Fatalf("expected zero decision on error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}
