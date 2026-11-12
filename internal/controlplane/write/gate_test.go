package write

import (
	"errors"
	"testing"
)

func TestRunGateRefusesWithReason(t *testing.T) {
	d, err := RunGate(false, "D0271")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.Allowed {
		t.Errorf("expected Allowed=false, got true")
	}
	if d.Reason != "D0271" {
		t.Errorf("expected Reason=D0271, got %q", d.Reason)
	}

	d2, err := RunGate(false, "custom reason")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d2.Allowed {
		t.Errorf("expected Allowed=false, got true")
	}
	if d2.Reason != "custom reason" {
		t.Errorf("expected Reason=custom reason, got %q", d2.Reason)
	}
}

func TestRunGateAllowsWhenOpen(t *testing.T) {
	cases := []string{"", "ignored"}
	for _, c := range cases {
		d, err := RunGate(true, c)
		if err != nil {
			t.Fatalf("unexpected error for reason %q: %v", c, err)
		}
		if !d.Allowed {
			t.Errorf("expected Allowed=true for reason %q, got false", c)
		}
		if d.Reason != "" {
			t.Errorf("expected empty Reason for reason %q, got %q", c, d.Reason)
		}
	}
}

func TestRunGateRefusesEmptyReason(t *testing.T) {
	cases := []string{"", " ", "\t", "\n"}
	for _, c := range cases {
		d, err := RunGate(false, c)
		if err == nil {
			t.Fatalf("expected error for reason %q, got nil", c)
		}
		if !errors.Is(err, ErrEmptyGateReason) {
			t.Errorf("expected ErrEmptyGateReason for reason %q, got %v", c, err)
		}
		if err.Error() != "D0271" {
			t.Errorf("expected error text D0271, got %q", err.Error())
		}
		if d.Allowed {
			t.Errorf("expected Allowed=false for reason %q, got true", c)
		}
	}
}
