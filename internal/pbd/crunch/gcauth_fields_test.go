package crunch

import (
	"reflect"
	"testing"
	"time"
)

func stubEstimator(evidence []GCQuotaEvidence) (time.Time, string, error) {
	return time.Now(), "stub", nil
}

// TestOwnerFlagPopulates verifies that a valid ticket populates ID and OwnerFlag.
func TestOwnerFlagPopulates(t *testing.T) {
	ev := []GCQuotaEvidence{{Usage: 1, Capacity: 10, ResetsAt: time.Now()}}
	auth, err := PopulateGCAuth(GCTicket{ID: "t-1", Gated: false}, ev, stubEstimator)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if auth.ID != "t-1" {
		t.Errorf("expected ID t-1, got %s", auth.ID)
	}
	if !auth.OwnerFlag {
		t.Error("expected OwnerFlag to be true")
	}
}

// TestCrunchSummaryOps verifies the exact ordered SummaryOps slice.
func TestCrunchSummaryOps(t *testing.T) {
	ev := []GCQuotaEvidence{{Usage: 1, Capacity: 10, ResetsAt: time.Now()}}
	auth, err := PopulateGCAuth(GCTicket{ID: "t-2", Gated: false}, ev, stubEstimator)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"build", "verify", "land"}
	if !reflect.DeepEqual(auth.SummaryOps, want) {
		t.Errorf("expected SummaryOps %v, got %v", want, auth.SummaryOps)
	}
	if len(auth.SummaryOps) != 3 {
		t.Errorf("expected 3 SummaryOps, got %d", len(auth.SummaryOps))
	}
}

// TestCrunchlogDigestNodes verifies the exact ordered DigestNodes slice.
func TestCrunchlogDigestNodes(t *testing.T) {
	ev := []GCQuotaEvidence{{Usage: 1, Capacity: 10, ResetsAt: time.Now()}}
	auth, err := PopulateGCAuth(GCTicket{ID: "t-3", Gated: false}, ev, stubEstimator)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"ticket", "usage"}
	if !reflect.DeepEqual(auth.DigestNodes, want) {
		t.Errorf("expected DigestNodes %v, got %v", want, auth.DigestNodes)
	}
	if len(auth.DigestNodes) != 2 {
		t.Errorf("expected 2 DigestNodes, got %d", len(auth.DigestNodes))
	}
}

// TestModelResolver verifies the Model field is set correctly.
func TestModelResolver(t *testing.T) {
	ev := []GCQuotaEvidence{{Usage: 1, Capacity: 10, ResetsAt: time.Now()}}
	auth, err := PopulateGCAuth(GCTicket{ID: "t-4", Gated: false}, ev, stubEstimator)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if auth.Model != "local-model" {
		t.Errorf("expected Model local-model, got %s", auth.Model)
	}
}

// TestHookContextGate verifies GateOpen is the inverse of Gated with no hook calls.
func TestHookContextGate(t *testing.T) {
	ev := []GCQuotaEvidence{{Usage: 1, Capacity: 10, ResetsAt: time.Now()}}

	authGated, err := PopulateGCAuth(GCTicket{ID: "t-5", Gated: true}, ev, stubEstimator)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if authGated.GateOpen {
		t.Error("expected GateOpen false when Gated is true")
	}

	authOpen, err := PopulateGCAuth(GCTicket{ID: "t-6", Gated: false}, ev, stubEstimator)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !authOpen.GateOpen {
		t.Error("expected GateOpen true when Gated is false")
	}
}
