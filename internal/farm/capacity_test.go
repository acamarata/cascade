package farm

import (
	"errors"
	"testing"
	"time"
)

// TestDeclarationsCompile verifies the stub contract declarations compile and
// the default stubs behave as zero-value placeholders.
func TestDeclarationsCompile(t *testing.T) {
	if RolePrimary != "primary" {
		t.Fatalf("RolePrimary = %q, want %q", RolePrimary, "primary")
	}
	if RoleAlwaysOn != "always-on" {
		t.Fatalf("RoleAlwaysOn = %q, want %q", RoleAlwaysOn, "always-on")
	}
	if RoleOpen != "open" {
		t.Fatalf("RoleOpen = %q, want %q", RoleOpen, "open")
	}

	row := &Capacity{
		Name:        "capacity-row",
		Role:        RolePrimary,
		CapPct:      42,
		SwapFloorMB: 128,
		LoadMax:     1.5,
		MeasuredPct: 33.0,
		UpdatedAt:   time.Unix(0, 0).UTC(),
	}

	snap := Snapshot{UsedPct: 12.5, SwapUsedMB: 16, Load: 0.25}
	got := Admit(row, snap)
	want := Decision{}
	if got != want {
		t.Fatalf("Admit(...) = %+v, want %+v", got, want)
	}

	Record(row, Sample{UsedPct: 34.5})

	if ErrNotImplemented == nil {
		t.Fatal("ErrNotImplemented is nil")
	}
	if got := ErrNotImplemented.Error(); got != "farm: operation unavailable" {
		t.Fatalf("ErrNotImplemented.Error() = %q, want %q", got, "farm: operation unavailable")
	}
	if !errors.Is(ErrNotImplemented, ErrNotImplemented) {
		t.Fatal("ErrNotImplemented does not match itself")
	}
}
