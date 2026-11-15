package log

import (
	"encoding/json"
	"testing"
)

func TestValidToolCallIncrementsStats(t *testing.T) {
	c := NewToolCounter(10)
	call := ToolCall{Name: "search", Args: json.RawMessage(`{"q":"test"}`)}
	ok := RecordToolCall(c, call)
	if !ok {
		t.Fatal("expected valid call to return true")
	}
	if c.Stats != 1 {
		t.Fatalf("expected stats=1, got %d", c.Stats)
	}
}

func TestValidToolCallLeavesRunCapUnchanged(t *testing.T) {
	c := NewToolCounter(10)
	call := ToolCall{Name: "search", Args: json.RawMessage(`{"q":"test"}`)}
	RecordToolCall(c, call)
	if c.Cap != 10 {
		t.Fatalf("expected cap=10 after valid call, got %d", c.Cap)
	}
}

func TestInvalidToolCallCountsNothing(t *testing.T) {
	c := NewToolCounter(10)
	call := ToolCall{Name: "", Args: json.RawMessage(`not-json`)}
	ok := RecordToolCall(c, call)
	if ok {
		t.Fatal("expected invalid call to return false")
	}
	if c.Stats != 0 {
		t.Fatalf("expected stats=0, got %d", c.Stats)
	}
	if c.Cap != 10 {
		t.Fatalf("expected cap=10, got %d", c.Cap)
	}
}

func TestManyToolCallsNeverExhaustCap(t *testing.T) {
	c := NewToolCounter(10)
	for i := 0; i < 100; i++ {
		call := ToolCall{Name: "fetch", Args: json.RawMessage(`{"url":"x"}`)}
		RecordToolCall(c, call)
	}
	if c.Stats != 100 {
		t.Fatalf("expected stats=100, got %d", c.Stats)
	}
	if c.Cap != 10 {
		t.Fatalf("expected cap=10, got %d", c.Cap)
	}
}
