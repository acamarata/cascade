package crunch

import "testing"

func TestClampWidthBelowMax(t *testing.T) {
	result := ClampWidth(5, 10)
	if result != 5 {
		t.Errorf("expected 5, got %d", result)
	}
}

func TestClampWidthEqualMax(t *testing.T) {
	result := ClampWidth(10, 10)
	if result != 10 {
		t.Errorf("expected 10, got %d", result)
	}
}

func TestClampWidthAboveMax(t *testing.T) {
	result := ClampWidth(15, 10)
	if result != 10 {
		t.Errorf("expected 10, got %d", result)
	}
}

func TestClampWidthZeroOrNegative(t *testing.T) {
	// Zero width should be clamped to 0 (or handled safely)
	result := ClampWidth(0, 10)
	if result < 0 {
		t.Errorf("expected non-negative result for zero input, got %d", result)
	}

	// Negative width should be handled safely (clamped to 0 or max)
	result = ClampWidth(-5, 10)
	if result < 0 {
		t.Errorf("expected non-negative result for negative input, got %d", result)
	}
}

func TestClampWidthNotNoGreenPass(t *testing.T) {
	// A clamped width must never be counted as a no-green pass.
	// This means when width is clamped to max, it should still be valid.
	result := ClampWidth(20, 10)
	if result != 10 {
		t.Errorf("expected clamped value 10, got %d", result)
	}
	// The result should be a valid positive width (not indicating failure/no-green-pass)
	if result <= 0 {
		t.Errorf("clamped width should be positive, got %d", result)
	}
}
