package rcl

import (
	"reflect"
	"testing"
)

// TestExactLaneMatchesTicketID verifies that a query containing a ticket ID
// returns the item carrying that ID.
func TestExactLaneMatchesTicketID(t *testing.T) {
	items := []LaneItem{
		{TicketID: "P2-RCL-1"},
		{TicketID: "P2-RCL-36"},
		{TicketID: "P2-RCL-99"},
	}
	got := ExactLane("see P2-RCL-36", items)
	want := []LaneItem{{TicketID: "P2-RCL-36"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ExactLane = %v, want %v", got, want)
	}
}

// TestExactLaneMatchesFileNameWithAndWithoutExt verifies that queries
// containing a filename with or without its extension return the matching
// item, and that boundary punctuation is trimmed while interior substrings
// do not match.
func TestExactLaneMatchesFileNameWithAndWithoutExt(t *testing.T) {
	items := []LaneItem{
		{FileName: "src/parse.go"},
		{FileName: "src/other.go"},
	}
	tests := []struct {
		query string
		want  []LaneItem
	}{
		{"parse.go", []LaneItem{{FileName: "src/parse.go"}}},
		{"parse", []LaneItem{{FileName: "src/parse.go"}}},
		{"(parse.go)", []LaneItem{{FileName: "src/parse.go"}}},
		{"[parse]", []LaneItem{{FileName: "src/parse.go"}}},
		{"pars", []LaneItem{}},
	}
	for _, tt := range tests {
		got := ExactLane(tt.query, items)
		want := tt.want
		if !reflect.DeepEqual(got, want) {
			t.Errorf("ExactLane(%q) = %v, want %v", tt.query, got, want)
		}
	}
}

// TestExactLaneMatchesPhaseID verifies that a query containing a phase ID
// returns items assigned to that phase.
func TestExactLaneMatchesPhaseID(t *testing.T) {
	items := []LaneItem{
		{PhaseID: "P1"},
		{PhaseID: "P2"},
		{PhaseID: "P2"},
		{PhaseID: "P3"},
	}
	got := ExactLane("phase P2", items)
	want := []LaneItem{
		{PhaseID: "P2"},
		{PhaseID: "P2"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ExactLane = %v, want %v", got, want)
	}
}

// TestExactLaneNoMatch verifies that a query lacking any matching token
// yields an empty, nonnil slice.
func TestExactLaneNoMatch(t *testing.T) {
	items := []LaneItem{{TicketID: "P2-RCL-1"}}
	got := ExactLane("hello", items)
	if len(got) != 0 {
		t.Fatalf("ExactLane = %v, want empty", got)
	}
	if got == nil {
		t.Fatalf("ExactLane returned nil, want nonnil empty slice")
	}
}
