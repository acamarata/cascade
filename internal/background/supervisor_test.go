package background

import (
	"strings"
	"testing"
)

func requireM1885Error(t *testing.T, err error) {
	t.Helper()

	if err == nil {
		t.Fatal("expected error M1885, got nil")
	}
	if !strings.Contains(err.Error(), "M1885") {
		t.Fatalf("expected error to contain code M1885, got: %v", err)
	}
}

func TestSupervisorReviewApprovesOnFirstRound(t *testing.T) {
	approved, err := SupervisorReview(1, true)
	if err != nil {
		t.Fatalf("SupervisorReview(1, true) returned unexpected error: %v", err)
	}
	if !approved {
		t.Fatal("SupervisorReview(1, true) = false, want true")
	}
}

func TestSupervisorReviewApprovesOnSecondRound(t *testing.T) {
	approved, err := SupervisorReview(2, true)
	if err != nil {
		t.Fatalf("SupervisorReview(2, true) returned unexpected error: %v", err)
	}
	if !approved {
		t.Fatal("SupervisorReview(2, true) = false, want true")
	}
}

func TestSupervisorReviewFailsAfterTwoRounds(t *testing.T) {
	approved, err := SupervisorReview(3, true)
	if approved {
		t.Fatal("SupervisorReview(3, true) approved = true, want false")
	}
	requireM1885Error(t, err)
}

func TestSupervisorReviewRejectsNonIndependentReviewer(t *testing.T) {
	approved, err := SupervisorReview(1, false)
	if approved {
		t.Fatal("SupervisorReview(1, false) approved = true, want false")
	}
	requireM1885Error(t, err)
}
