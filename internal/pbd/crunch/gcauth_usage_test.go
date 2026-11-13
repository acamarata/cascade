package crunch

import (
	"errors"
	"testing"
	"time"
)

func TestUsageSamplerFreshEvidence(t *testing.T) {
	resetTime := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	calls := 0
	var received []GCQuotaEvidence
	est := func(ev []GCQuotaEvidence) (time.Time, string, error) {
		calls++
		received = ev
		return resetTime, "quota_window", nil
	}
	evidence := []GCQuotaEvidence{
		{Usage: 100, Capacity: 100, ResetsAt: resetTime, Stale: false},
	}
	auth, err := PopulateGCAuth(GCTicket{ID: "t1", Gated: false}, evidence, est)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected 1 estimator call, got %d", calls)
	}
	if len(auth.Samples) != 1 {
		t.Fatalf("expected 1 sample, got %d", len(auth.Samples))
	}
	if !auth.Samples[0].ResetAt.Equal(resetTime) {
		t.Errorf("expected reset time %v, got %v", resetTime, auth.Samples[0].ResetAt)
	}
	if auth.Samples[0].Source != "quota_window" {
		t.Errorf("expected source quota_window, got %s", auth.Samples[0].Source)
	}
	// Verify defensive copy: mutating original should not affect received
	evidence[0].Usage = 0
	if received[0].Usage == 0 {
		t.Error("estimator did not receive a defensive copy")
	}
}

func TestUsageSamplerStaleEvidencePreserved(t *testing.T) {
	subTime := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	est := func(ev []GCQuotaEvidence) (time.Time, string, error) {
		if len(ev) != 1 || !ev[0].Stale {
			t.Error("stale flag not forwarded")
		}
		return subTime, "subscription", nil
	}
	evidence := []GCQuotaEvidence{
		{Usage: 50, Capacity: 100, ResetsAt: subTime, Stale: true},
	}
	auth, err := PopulateGCAuth(GCTicket{ID: "t2", Gated: false}, evidence, est)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(auth.Samples) != 1 {
		t.Fatalf("expected 1 sample, got %d", len(auth.Samples))
	}
	if !auth.Samples[0].ResetAt.Equal(subTime) {
		t.Errorf("expected subscription time, got %v", auth.Samples[0].ResetAt)
	}
	if auth.Samples[0].Source != "subscription" {
		t.Errorf("expected source subscription, got %s", auth.Samples[0].Source)
	}
}

func TestUsageSamplerZeroResetEmptySource(t *testing.T) {
	est := func(_ []GCQuotaEvidence) (time.Time, string, error) {
		return time.Time{}, "", nil
	}
	evidence := []GCQuotaEvidence{
		{Usage: 0, Capacity: 100, ResetsAt: time.Time{}, Stale: false},
	}
	auth, err := PopulateGCAuth(GCTicket{ID: "t3", Gated: false}, evidence, est)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(auth.Samples) != 1 {
		t.Fatalf("expected 1 sample, got %d", len(auth.Samples))
	}
	if !auth.Samples[0].ResetAt.IsZero() {
		t.Error("expected zero reset time")
	}
	if auth.Samples[0].Source != "" {
		t.Error("expected empty source")
	}
}

func TestUsageSamplerRepeatedEvidenceInOrder(t *testing.T) {
	est := func(ev []GCQuotaEvidence) (time.Time, string, error) {
		if len(ev) != 3 {
			t.Fatalf("expected 3 evidence entries, got %d", len(ev))
		}
		for i := 1; i < len(ev); i++ {
			if ev[i] != ev[0] {
				t.Errorf("evidence entry %d differs from first", i)
			}
		}
		return time.Time{}, "", nil
	}
	ev := GCQuotaEvidence{Usage: 10, Capacity: 20, ResetsAt: time.Time{}, Stale: true}
	evidence := []GCQuotaEvidence{ev, ev, ev}
	_, err := PopulateGCAuth(GCTicket{ID: "t4", Gated: false}, evidence, est)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestUsageSamplerEstimatorError(t *testing.T) {
	cause := errors.New("underlying failure")
	est := func(_ []GCQuotaEvidence) (time.Time, string, error) {
		return time.Time{}, "", cause
	}
	evidence := []GCQuotaEvidence{
		{Usage: 0, Capacity: 100, ResetsAt: time.Time{}, Stale: false},
	}
	auth, err := PopulateGCAuth(GCTicket{ID: "t5", Gated: false}, evidence, est)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrGCUsageEstimate) {
		t.Error("error does not wrap ErrGCUsageEstimate")
	}
	if !errors.Is(err, cause) {
		t.Error("error does not wrap cause")
	}
	if auth.ID != "" || auth.OwnerFlag || auth.GateOpen || auth.Model != "" || len(auth.SummaryOps) != 0 || len(auth.DigestNodes) != 0 || len(auth.Samples) != 0 {
		t.Error("expected zero GCAuth on error")
	}
}
