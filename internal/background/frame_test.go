package background

import (
	"strings"
	"testing"
)

func TestFrameMessageStartsWithTag(t *testing.T) {
	msg, err := FrameMessage(2, 5, "go")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(msg, Tag) {
		t.Errorf("message %q does not start with Tag %q", msg, Tag)
	}
	if !strings.Contains(msg, "turn 2 of 5") {
		t.Errorf("message %q does not contain 'turn 2 of 5'", msg)
	}
}

func TestFrameMessageRoundTrip(t *testing.T) {
	msg, err := FrameMessage(2, 5, "go")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	n, m, body, err := ParseMessage(msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 2 {
		t.Errorf("n = %d, want 2", n)
	}
	if m != 5 {
		t.Errorf("m = %d, want 5", m)
	}
	if body != "go" {
		t.Errorf("body = %q, want %q", body, "go")
	}
}

func TestFrameMessageRejectsBadTurns(t *testing.T) {
	_, err := FrameMessage(0, 5, "go")
	if err == nil {
		t.Errorf("FrameMessage(0, 5, \"go\") should return error")
	}
	_, err = FrameMessage(6, 5, "go")
	if err == nil {
		t.Errorf("FrameMessage(6, 5, \"go\") should return error")
	}
}

func TestParseMessageMissingTagFails(t *testing.T) {
	_, _, _, err := ParseMessage("turn 2 of 5 go")
	if err == nil {
		t.Errorf("ParseMessage without tag should return error")
	}
}
