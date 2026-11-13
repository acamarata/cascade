package crunch

import (
	"reflect"
	"testing"
)

func TestReseedPendingInjectsRecap(t *testing.T) {
	history := []HistoryItem{
		{Role: "user", Text: "msg1"},
		{Role: "assistant", Text: "msg2"},
		{Role: "user", Text: "msg3"},
	}
	payload := CodePayload{
		Text:        "original text",
		ReseedRecap: ReseedRecap{{Role: "old", Text: "old text"}},
	}

	result := InjectReseedRecap(payload, true, history)

	if result.Text != payload.Text {
		t.Fatalf("expected Text to be preserved, got %q", result.Text)
	}
	if len(result.ReseedRecap) != 3 {
		t.Fatalf("expected 3 items in recap, got %d", len(result.ReseedRecap))
	}
	for i, item := range result.ReseedRecap {
		if !reflect.DeepEqual(item, history[i]) {
			t.Fatalf("recap item %d mismatch: got %v, want %v", i, item, history[i])
		}
	}

	// Verify copy semantics: modifying result shouldn't affect history
	result.ReseedRecap[0].Text = "mutated"
	if history[0].Text == "mutated" {
		t.Fatal("recap aliases history slice elements")
	}

	// Input payload unchanged
	if len(payload.ReseedRecap) != 1 {
		t.Fatal("input payload was mutated")
	}
	if payload.ReseedRecap[0].Text != "old text" {
		t.Fatal("input payload recap was mutated")
	}
}

func TestReseedRecapM1286Limit(t *testing.T) {
	history := []HistoryItem{
		{Role: "user", Text: "msg1"},
		{Role: "assistant", Text: "msg2"},
		{Role: "user", Text: "msg3"},
		{Role: "assistant", Text: "msg4"},
		{Role: "user", Text: "msg5"},
	}
	payload := CodePayload{Text: "text"}

	result := InjectReseedRecap(payload, true, history)

	if len(result.ReseedRecap) != M1286RecapLimit {
		t.Fatalf("expected %d items, got %d", M1286RecapLimit, len(result.ReseedRecap))
	}
	// Expect items 3, 4, 5 (indices 2, 3, 4)
	expected := history[2:]
	for i, item := range result.ReseedRecap {
		if !reflect.DeepEqual(item, expected[i]) {
			t.Fatalf("recap item %d mismatch: got %v, want %v", i, item, expected[i])
		}
	}
}
