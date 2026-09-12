package topology

import (
	"errors"
	"testing"
	"time"
)

// TestSelectSkipsExpiredObservations is the named acceptance test: a stale
// provider-status snapshot loses to a live cli-observation.
func TestSelectSkipsExpiredObservations(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	stale := Bucket{Name: "rpm", Source: SourceProviderStatus, ObservedAt: now.Add(-15 * time.Minute)} // expires at 10m
	live := Bucket{Name: "rpm", Source: SourceCLIObservation, ObservedAt: now.Add(-5 * time.Minute)}   // fresh (expires at 30m)

	got, err := Select([]Bucket{stale, live}, now)
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if got.Source != SourceCLIObservation {
		t.Errorf("Select = %v, want the live cli-observation to win over the stale provider-status", got.Source)
	}
}

func TestSelectAllExpiredReportsUnknown(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	stale := Bucket{Name: "rpm", Limit: 100, LimitScopeID: "scope:x", Source: SourceProviderStatus, ObservedAt: now.Add(-time.Hour), Confidence: 0.9}
	got, err := Select([]Bucket{stale}, now)
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if got.Source != SourceUnknown {
		t.Errorf("all-expired Select.Source = %v, want SourceUnknown", got.Source)
	}
	if got.Confidence != 0 {
		t.Errorf("all-expired Select.Confidence = %v, want 0", got.Confidence)
	}
	if got.Limit != 100 || got.LimitScopeID != "scope:x" {
		t.Error("all-expired fallback must keep the bucket's concurrency caps and scope")
	}
}

func TestSelectEmptyIsInvariant(t *testing.T) {
	if _, err := Select(nil, time.Now()); !errors.Is(err, ErrTopologyInvariant) {
		t.Errorf("Select(nil): err = %v, want ErrTopologyInvariant", err)
	}
}

func TestUnknownSourceNeverExpires(t *testing.T) {
	old := Bucket{Source: SourceUnknown, ObservedAt: time.Unix(0, 0)}
	if Expired(old, time.Now().Add(1000*24*time.Hour)) {
		t.Error("SourceUnknown must never expire")
	}
}

func TestExpiredPerSourceThresholds(t *testing.T) {
	now := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		source    BucketSource
		age       time.Duration
		wantStale bool
	}{
		{SourceProviderStatus, 9 * time.Minute, false},
		{SourceProviderStatus, 11 * time.Minute, true},
		{SourceCLIObservation, 29 * time.Minute, false},
		{SourceCLIObservation, 31 * time.Minute, true},
		{SourceUserEstimate, 23 * time.Hour, false},
		{SourceUserEstimate, 25 * time.Hour, true},
	}
	for _, c := range cases {
		b := Bucket{Source: c.source, ObservedAt: now.Add(-c.age)}
		if got := Expired(b, now); got != c.wantStale {
			t.Errorf("Expired(%v, age=%v) = %v, want %v", c.source, c.age, got, c.wantStale)
		}
	}
}

func TestSlidingWindowSumOnlyCountsWithinWindow(t *testing.T) {
	now := time.Date(2026, 9, 1, 0, 1, 0, 0, time.UTC)
	events := []ConsumptionEvent{
		{At: now.Add(-2 * time.Minute), Amount: 100}, // outside 60s window
		{At: now.Add(-30 * time.Second), Amount: 5},
		{At: now.Add(-10 * time.Second), Amount: 3},
		{At: now.Add(time.Second), Amount: 999}, // in the future, excluded
	}
	got := SlidingWindowSum(events, now, 60*time.Second)
	if got != 8 {
		t.Errorf("SlidingWindowSum = %d, want 8", got)
	}
}
