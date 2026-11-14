package background

import "testing"

// TestEmptyBacklogNonEmptySkips verifies that OnEmptyBacklog returns
// Item{},false without mutation or logging when the backlog already
// contains items, even if health findings and log lines are provided.
func TestEmptyBacklogNonEmptySkips(t *testing.T) {
	b := &Backlog{
		Items: []Item{{Title: "existing", Evidence: "proof"}},
	}
	health := HealthReading{
		Findings: []Finding{{Summary: "unhealthy", Evidence: "metric"}},
	}
	logs := []string{"some log line"}

	logCalls := 0
	logger := func(_ string) {
		logCalls++
	}

	got, ok := OnEmptyBacklog(b, health, logs, logger)
	if ok {
		t.Fatalf("expected ok=false for non-empty backlog, got true")
	}
	if got != (Item{}) {
		t.Fatalf("expected zero Item, got %+v", got)
	}
	if len(b.Items) != 1 || b.Items[0].Title != "existing" || b.Items[0].Evidence != "proof" {
		t.Fatalf("backlog was mutated: %+v", b.Items)
	}
	if logCalls != 0 {
		t.Fatalf("expected zero log calls, got %d", logCalls)
	}
}
