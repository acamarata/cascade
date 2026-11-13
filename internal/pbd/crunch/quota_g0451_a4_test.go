package crunch

import (
	"errors"
	"testing"
)

// TestQuotaMapA4 verifies that MapQuota("A4",95,false) returns Alloc{"A4",90,90},nil
// and that repeated requests succeed independently.
func TestQuotaMapA4(t *testing.T) {
	got, err := MapQuota("A4", 95, false)
	if err != nil {
		t.Fatalf("MapQuota(\"A4\", 95, false) returned error: %v", err)
	}
	want := Alloc{Account: "A4", FiveHourPct: 90, WeeklyPct: 90}
	if got != want {
		t.Errorf("MapQuota(\"A4\", 95, false) = %+v, want %+v", got, want)
	}

	// Repeated requests succeed independently
	got2, err2 := MapQuota("A4", 95, false)
	if err2 != nil {
		t.Fatalf("second MapQuota(\"A4\", 95, false) returned error: %v", err2)
	}
	if got2 != want {
		t.Errorf("second MapQuota(\"A4\", 95, false) = %+v, want %+v", got2, want)
	}
}

// TestEnforce90OnA4FullTilt verifies that A4 with pct 90 or 100 and fullTilt=true
// returns Alloc{"A4",90,90} and nil error.
func TestEnforce90OnA4FullTilt(t *testing.T) {
	tests := []struct {
		name string
		pct  int
	}{
		{"pct 90", 90},
		{"pct 100", 100},
	}
	want := Alloc{Account: "A4", FiveHourPct: 90, WeeklyPct: 90}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := MapQuota("A4", tt.pct, true)
			if err != nil {
				t.Fatalf("MapQuota(\"A4\", %d, true) returned error: %v", tt.pct, err)
			}
			if got != want {
				t.Errorf("MapQuota(\"A4\", %d, true) = %+v, want %+v", tt.pct, got, want)
			}
		})
	}
}

// TestRejectA4Below90 verifies that A4 with pct 89 and either fullTilt value
// returns zero allocation and errors.Is(err, ErrT0308).
func TestRejectA4Below90(t *testing.T) {
	tests := []struct {
		name     string
		fullTilt bool
	}{
		{"fullTilt false", false},
		{"fullTilt true", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := MapQuota("A4", 89, tt.fullTilt)
			if !errors.Is(err, ErrT0308) {
				t.Errorf("MapQuota(\"A4\", 89, %v) error = %v, want errors.Is(err, ErrT0308)", tt.fullTilt, err)
			}
			want := Alloc{}
			if got != want {
				t.Errorf("MapQuota(\"A4\", 89, %v) = %+v, want empty Alloc", tt.fullTilt, got)
			}
		})
	}
}
