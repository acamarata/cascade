package crunch

import (
	"errors"
	"testing"
)

// TestRouteCrunchToGcsat verifies that crunch-op-a routes to gatewaysat.
func TestRouteCrunchToGcsat(t *testing.T) {
	target, err := Route("crunch-op-a")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if target != "gatewaysat" {
		t.Fatalf("expected gatewaysat, got %s", target)
	}
}

// TestRouteCrunchToGcsched verifies that crunch-op-b routes to gatewaysched.
func TestRouteCrunchToGcsched(t *testing.T) {
	target, err := Route("crunch-op-b")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if target != "gatewaysched" {
		t.Fatalf("expected gatewaysched, got %s", target)
	}
}

// TestRejectBrokerQueueRouting verifies that broker-route is rejected with ErrD0583.
func TestRejectBrokerQueueRouting(t *testing.T) {
	target, err := Route("broker-route")
	if target != "" {
		t.Fatalf("expected empty target, got %s", target)
	}
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrD0583) {
		t.Fatalf("expected ErrD0583, got %v", err)
	}
}

// TestInvalidOperationError verifies that unknown operations are rejected with ErrD0583.
func TestInvalidOperationError(t *testing.T) {
	target, err := Route("unknown-op")
	if target != "" {
		t.Fatalf("expected empty target, got %s", target)
	}
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrD0583) {
		t.Fatalf("expected ErrD0583, got %v", err)
	}
}
