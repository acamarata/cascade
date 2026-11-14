package crunch

import (
	"errors"
	"testing"
)

// TestQuotaMapAcctC verifies MapQuota for account acct-c and validation cases.
func TestQuotaMapAcctC(t *testing.T) {
	got, err := MapQuota("acct-c", 75, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := Alloc{Account: "acct-c", FiveHourPct: 75, WeeklyPct: 75}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}

	tests := []struct {
		name    string
		acct    string
		pct     int
		wantErr bool
	}{
		{"pct zero", "acct-c", 0, false},
		{"pct hundred", "acct-c", 100, false},
		{"unknown account", "XX", 50, true},
		{"blank account", "", 50, true},
		{"pct negative", "acct-c", -1, true},
		{"pct over", "acct-c", 101, true},
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

// TestQuotaMapTertiary verifies MapQuota for account tertiary.
func TestQuotaMapTertiary(t *testing.T) {
	got, err := MapQuota("tertiary", 80, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := Alloc{Account: "tertiary", FiveHourPct: 80, WeeklyPct: 80}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}
