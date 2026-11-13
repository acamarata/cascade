package budget

import (
	"errors"
	"testing"
)

// TestCheckWithinBudget verifies that usage within all limits returns nil and no alert.
func TestCheckWithinBudget(t *testing.T) {
	var alerts []Alert
	limits := map[string]Limits{
		"Accept": {MemBytes: 64 << 20, CPUMs: 500, TimeMs: 1000},
	}
	c := New(limits, func(a Alert) { alerts = append(alerts, a) })

	usage := Usage{MemBytes: 10 << 20, CPUMs: 100, TimeMs: 200}
	err := c.Check("Accept", usage)

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(alerts) != 0 {
		t.Fatalf("expected no alerts, got %d", len(alerts))
	}
}

// TestCheckMemoryExceededAlerts verifies that memory over limit returns error and records alert.
func TestCheckMemoryExceededAlerts(t *testing.T) {
	var alerts []Alert
	limits := map[string]Limits{
		"shapes": {MemBytes: 64 << 20, CPUMs: 500, TimeMs: 1000},
	}
	c := New(limits, func(a Alert) { alerts = append(alerts, a) })

	usage := Usage{MemBytes: 100 << 20}
	err := c.Check("shapes", usage)

	if !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("expected ErrBudgetExceeded, got %v", err)
	}
	if len(alerts) != 1 {
		t.Fatalf("expected 1 alert, got %d", len(alerts))
	}
	if alerts[0].Resource != "memory" {
		t.Fatalf("expected alert for memory, got %s", alerts[0].Resource)
	}
}

// TestCheckTimeExceededThrottles verifies that time over limit returns error and sets Throttle.
func TestCheckTimeExceededThrottles(t *testing.T) {
	var alerts []Alert
	limits := map[string]Limits{
		"Accept": {MemBytes: 64 << 20, CPUMs: 500, TimeMs: 1000},
	}
	c := New(limits, func(a Alert) { alerts = append(alerts, a) })

	usage := Usage{TimeMs: 2000}
	err := c.Check("Accept", usage)

	if !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("expected ErrBudgetExceeded, got %v", err)
	}
	if !c.Throttle {
		t.Fatalf("expected Throttle=true")
	}
}

// TestCheckUnknownModuleFailsClosed verifies that unknown module returns ErrUnknownModule.
func TestCheckUnknownModuleFailsClosed(t *testing.T) {
	var alerts []Alert
	limits := map[string]Limits{
		"Accept": {MemBytes: 64 << 20, CPUMs: 500, TimeMs: 1000},
	}
	c := New(limits, func(a Alert) { alerts = append(alerts, a) })

	err := c.Check("Bogus", Usage{})

	if !errors.Is(err, ErrUnknownModule) {
		t.Fatalf("expected ErrUnknownModule, got %v", err)
	}
}
