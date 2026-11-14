// Package hj provides nodelink reconciliation logic.
package hj

import (
	"testing"
)

// TestReconcileRevokesUndispatchedOffer verifies that undispatched offers are revoked.
func TestReconcileRevokesUndispatchedOffer(t *testing.T) {
	state := State{
		Init: true,
		Offers: []Offer{
			{ID: "offer-1", Dispatched: false},
		},
	}

	result, err := Reconcile(state)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.Offers) != 0 {
		t.Errorf("expected 0 active offers, got %d", len(result.Offers))
	}
}

// TestReconcileKeepsDispatchedAttempt verifies that dispatched attempts are kept.
func TestReconcileKeepsDispatchedAttempt(t *testing.T) {
	state := State{
		Init: true,
		Attempts: []Attempt{
			{ID: "attempt-1"},
		},
		Offers: []Offer{
			{ID: "offer-1", Dispatched: true},
		},
	}

	result, err := Reconcile(state)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.Attempts) != 1 {
		t.Errorf("expected 1 active attempt, got %d", len(result.Attempts))
	}
}

// TestReconcileEmptyStateNoOp verifies that empty state returns empty state.
func TestReconcileEmptyStateNoOp(t *testing.T) {
	state := State{
		Init: true,
	}

	result, err := Reconcile(state)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.Offers) != 0 {
		t.Errorf("expected 0 offers, got %d", len(result.Offers))
	}
	if len(result.Attempts) != 0 {
		t.Errorf("expected 0 attempts, got %d", len(result.Attempts))
	}
}

// TestReconcileInvalidInputFailsClosed verifies that nil state returns error D0913.
func TestReconcileInvalidInputFailsClosed(t *testing.T) {
	state := State{}

	_, err := Reconcile(state)
	if err == nil {
		t.Fatal("expected error for invalid state, got nil")
	}

	if err.Error() != "D0913: invalid state" {
		t.Errorf("expected error 'D0913: invalid state', got '%v'", err)
	}
}
