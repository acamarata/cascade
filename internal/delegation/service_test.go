package delegation

import (
	"context"
	"testing"
	"time"
)

// fakeClock implements runtime.Clock for tests.
type fakeClock struct{ now time.Time }

func (f fakeClock) Now() time.Time { return f.now }

// TestDelegateAppliesRequestDefaults verifies default values are populated.
func TestDelegateAppliesRequestDefaults(t *testing.T) {
	clk := fakeClock{now: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
	svc := NewService(Deps{Clock: clk})

	req := &Request{AgentID: "agent-1"}
	_, err := svc.Delegate(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if req.Deadline == "" {
		t.Error("expected Deadline to be defaulted")
	}
	if req.Need == "" {
		t.Error("expected Need to be defaulted")
	}
	if req.MinQuality == "" {
		t.Error("expected MinQuality to be defaulted")
	}
}
