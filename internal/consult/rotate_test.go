package consult

import (
	"errors"
	"testing"
)

func TestShouldRotateBelowThreshold(t *testing.T) {
	rotate, err := ShouldRotate(1200, 1000, 500)
	if err != nil {
		t.Fatalf("ShouldRotate returned unexpected error: %v", err)
	}
	if rotate {
		t.Fatalf("ShouldRotate = true, want false for growth at or below threshold")
	}
}

func TestShouldRotateAboveThreshold(t *testing.T) {
	rotate, err := ShouldRotate(1600, 1000, 500)
	if err != nil {
		t.Fatalf("ShouldRotate returned unexpected error: %v", err)
	}
	if !rotate {
		t.Fatalf("ShouldRotate = false, want true for growth above threshold")
	}
}

func TestShouldRotateAtThresholdExact(t *testing.T) {
	rotate, err := ShouldRotate(1500, 1000, 500)
	if err != nil {
		t.Fatalf("ShouldRotate returned unexpected error: %v", err)
	}
	if rotate {
		t.Fatalf("ShouldRotate = true, want false for growth exactly at threshold")
	}
}

func TestShouldRotateInvalidInputFailsClosed(t *testing.T) {
	rotate, err := ShouldRotate(800, 1000, 500)
	if rotate {
		t.Fatalf("ShouldRotate = true, want false for invalid input")
	}
	if err == nil {
		t.Fatalf("ShouldRotate returned nil error for invalid input")
	}
	if !errors.Is(err, ErrInvalidRotationInput) {
		t.Fatalf("ShouldRotate error = %v, want %v", err, ErrInvalidRotationInput)
	}
}
