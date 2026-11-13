package crunch

import (
	"testing"
)

// TestReseedRecapEmptyHistory verifies that a pending reseed with nil or empty
// history clears any existing recap to nil. Empty and duplicate item strings
// must otherwise be preserved, and caller data must never be mutated.
func TestReseedRecapEmptyHistory(t *testing.T) {
	// Test with nil history
	payload1 := CodePayload{
		Text:        "hello",
		ReseedRecap: ReseedRecap{{Role: "user", Text: "old"}},
	}
	result1 := InjectReseedRecap(payload1, true, nil)
	if result1.ReseedRecap != nil {
		t.Errorf("expected nil recap for nil history, got %v", result1.ReseedRecap)
	}
	if result1.Text != "hello" {
		t.Errorf("expected text unchanged, got %s", result1.Text)
	}

	// Test with empty history
	payload2 := CodePayload{
		Text:        "world",
		ReseedRecap: ReseedRecap{{Role: "user", Text: "old"}},
	}
	result2 := InjectReseedRecap(payload2, true, []HistoryItem{})
	if result2.ReseedRecap != nil {
		t.Errorf("expected nil recap for empty history, got %v", result2.ReseedRecap)
	}
	if result2.Text != "world" {
		t.Errorf("expected text unchanged, got %s", result2.Text)
	}

	// Test that it preserves empty and duplicate item strings
	hist3 := []HistoryItem{{Role: "user", Text: ""}, {Role: "user", Text: ""}}
	payload3 := CodePayload{Text: "test3"}
	result3 := InjectReseedRecap(payload3, true, hist3)
	if result3.ReseedRecap == nil || len(result3.ReseedRecap) != 2 {
		t.Fatalf("expected 2 items in recap, got %v", result3.ReseedRecap)
	}
	if len(result3.ReseedRecap) != len(hist3) {
		t.Fatalf("recap length mismatch: got %d, want %d", len(result3.ReseedRecap), len(hist3))
	}
	for i := range hist3 {
		if result3.ReseedRecap[i].Role != hist3[i].Role || result3.ReseedRecap[i].Text != hist3[i].Text {
			t.Errorf("recap[%d] = {%q,%q}, want {%q,%q}", i, result3.ReseedRecap[i].Role, result3.ReseedRecap[i].Text, hist3[i].Role, hist3[i].Text)
		}
	}

	// Test that it does not mutate caller history
	hist4 := []HistoryItem{{Role: "user", Text: "h1"}}
	payload4 := CodePayload{Text: "test4", ReseedRecap: ReseedRecap{{Role: "user", Text: "old"}}}
	InjectReseedRecap(payload4, true, hist4)
	if len(hist4) != 1 || hist4[0].Text != "h1" {
		t.Errorf("caller history was mutated")
	}

	// Test that it does not mutate existing recap storage
	origRecap := ReseedRecap{{Role: "user", Text: "old"}}
	payload5 := CodePayload{Text: "test5", ReseedRecap: origRecap}
	InjectReseedRecap(payload5, true, nil)
	if len(origRecap) != 1 || origRecap[0].Text != "old" {
		t.Errorf("existing recap storage was mutated")
	}

	// Test reseedPending=false leaves everything alone
	payload6 := CodePayload{
		Text:        "unchanged",
		ReseedRecap: ReseedRecap{{Role: "user", Text: "keep"}},
	}
	result6 := InjectReseedRecap(payload6, false, nil)
	if result6.ReseedRecap == nil || len(result6.ReseedRecap) != 1 {
		t.Errorf("expected recap preserved when pending=false, got %v", result6.ReseedRecap)
	}
}
