package ladder

import (
	"errors"
	"testing"
)

func TestOrderDeclarations(t *testing.T) {
	r := Rung{
		Name:       "test",
		Kind:       KindGC,
		ResetHours: 24,
		Room:       true,
		Stale:      false,
	}
	if r.Name != "test" {
		t.Errorf("Rung.Name = %q, want %q", r.Name, "test")
	}
	if r.Kind != KindGC {
		t.Errorf("Rung.Kind = %q, want %q", r.Kind, KindGC)
	}
	if r.ResetHours != 24 {
		t.Errorf("Rung.ResetHours = %d, want %d", r.ResetHours, 24)
	}
	if !r.Room {
		t.Errorf("Rung.Room = %v, want true", r.Room)
	}
	if r.Stale {
		t.Errorf("Rung.Stale = %v, want false", r.Stale)
	}

	if KindGC != "gc" {
		t.Errorf("KindGC = %q, want %q", KindGC, "gc")
	}
	if KindGP != "gp" {
		t.Errorf("KindGP = %q, want %q", KindGP, "gp")
	}
	if KindCO != "co" {
		t.Errorf("KindCO = %q, want %q", KindCO, "co")
	}
	if KindClaude != "claude" {
		t.Errorf("KindClaude = %q, want %q", KindClaude, "claude")
	}
	if KindReserved != "reserved" {
		t.Errorf("KindReserved = %q, want %q", KindReserved, "reserved")
	}

	if ErrNotImplemented == nil {
		t.Error("ErrNotImplemented is nil, want non-nil")
	}
	if ErrNotImplemented.Error() != "ladder: operation unavailable" {
		t.Errorf("ErrNotImplemented.Error() = %q, want %q", ErrNotImplemented.Error(), "ladder: operation unavailable")
	}
	if !errors.Is(ErrNotImplemented, ErrNotImplemented) {
		t.Error("errors.Is(ErrNotImplemented, ErrNotImplemented) failed")
	}

	out := Order(nil)
	if out != nil {
		t.Errorf("Order(nil) = %v, want nil", out)
	}
}
