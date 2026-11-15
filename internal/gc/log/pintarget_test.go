package log

import (
	"testing"
)

func TestPinTargetOverridesRamp(t *testing.T) {
	ramp := func() int { return 12 }
	p := NewPinTarget(ramp)

	err := p.SetTarget(5)
	if err != nil {
		t.Fatalf("SetTarget(5) returned error: %v", err)
	}

	got := p.Target()
	if got != 5 {
		t.Errorf("Target() = %d, want 5", got)
	}

	if !p.Pinned() {
		t.Error("Pinned() = false, want true")
	}
}

func TestPinTargetZeroResumesRamp(t *testing.T) {
	ramp := func() int { return 12 }
	p := NewPinTarget(ramp)

	err := p.SetTarget(5)
	if err != nil {
		t.Fatalf("SetTarget(5) returned error: %v", err)
	}

	err = p.SetTarget(0)
	if err != nil {
		t.Fatalf("SetTarget(0) returned error: %v", err)
	}

	got := p.Target()
	if got != 12 {
		t.Errorf("Target() = %d, want 12", got)
	}

	if p.Pinned() {
		t.Error("Pinned() = true, want false")
	}
}

func TestPinTargetNegativeFails(t *testing.T) {
	ramp := func() int { return 12 }
	p := NewPinTarget(ramp)

	err := p.SetTarget(5)
	if err != nil {
		t.Fatalf("SetTarget(5) returned error: %v", err)
	}

	err = p.SetTarget(-1)
	if err == nil {
		t.Fatal("SetTarget(-1) returned nil error, want error")
	}

	// Previous pin should be kept
	got := p.Target()
	if got != 5 {
		t.Errorf("Target() = %d, want 5 (previous pin should be kept)", got)
	}

	if !p.Pinned() {
		t.Error("Pinned() = false, want true (previous pin should be kept)")
	}
}

func TestPinTargetDefaultUsesRamp(t *testing.T) {
	ramp := func() int { return 12 }
	p := NewPinTarget(ramp)

	got := p.Target()
	if got != 12 {
		t.Errorf("Target() = %d, want 12", got)
	}

	if p.Pinned() {
		t.Error("Pinned() = true, want false")
	}
}

func TestPinTargetNegativeError(t *testing.T) {
	ramp := func() int { return 12 }
	p := NewPinTarget(ramp)
	err := p.SetTarget(-1)
	if err == nil {
		t.Fatal("SetTarget(-1) returned nil error, want error")
	}
}
