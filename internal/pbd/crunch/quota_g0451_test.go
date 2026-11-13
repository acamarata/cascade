package crunch

import (
	"errors"
	"testing"
)

// TestQuotaMapA3 verifies MapQuota for account A3 and validation cases.
func TestQuotaMapA3(t *testing.T) {
	got, err := MapQuota("A3", 75, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := Alloc{Account: "A3", FiveHourPct: 75, WeeklyPct: 75}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}

	tests := []struct {
		name    string
		acct    string
		pct     int
		wantErr bool
	}{
		{"pct zero", "A3", 0, false},
		{"pct hundred", "A3", 100, false},
		{"unknown account", "XX", 50, true},
		{"blank account", "", 50, true},
		{"pct negative", "A3", -1, true},
		{"pct over", "A3", 101, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, err := MapQuota(tt.acct, tt.pct, false)
			if tt.wantErr {
				if !errors.Is(err, ErrT0308) {
					t.Fatalf("expected ErrT0308, got %v", err)
				}
				if a != (Alloc{}) {
					t.Fatalf("expected zero Alloc, got %+v", a)
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				want := Alloc{Account: tt.acct, FiveHourPct: tt.pct, WeeklyPct: tt.pct}
				if a != want {
					t.Fatalf("got %+v, want %+v", a, want)
				}
			}
		})
	}
}

// TestQuotaMapCO verifies MapQuota for account CO.
func TestQuotaMapCO(t *testing.T) {
	got, err := MapQuota("CO", 80, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := Alloc{Account: "CO", FiveHourPct: 80, WeeklyPct: 80}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}
