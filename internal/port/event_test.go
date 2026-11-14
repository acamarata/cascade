package port

import (
	"testing"
)

func TestEventZeroValue(t *testing.T) {
	var event Event

	if event.Name != "" {
		t.Fatalf("Event.Name = %q, want empty string", event.Name)
	}
	if event.Version != 0 {
		t.Fatalf("Event.Version = %d, want 0", event.Version)
	}
	if event.CausationID != "" {
		t.Fatalf("Event.CausationID = %q, want empty string", event.CausationID)
	}
	if event.CausationDepth != 0 {
		t.Fatalf("Event.CausationDepth = %d, want 0", event.CausationDepth)
	}
	if event.Payload != nil {
		t.Fatalf("Event.Payload = %#v, want nil", event.Payload)
	}
}
