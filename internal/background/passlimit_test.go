package background

import (
	"errors"
	"testing"
)

func TestPassLimitReadErrorGivesOne(t *testing.T) {
	readErr := errors.New("cpu read failed")
	read := func() (float64, error) {
		return 0, readErr
	}
	headroom := func(cpu float64) int {
		t.Fatal("headroom should not be called on read error")
		return 0
	}

	result := PassLimit(read, headroom)
	if result != 1 {
		t.Errorf("expected 1, got %d", result)
	}
}

func TestPassLimitUsesHeadroomOnSuccess(t *testing.T) {
	read := func() (float64, error) {
		return 20.0, nil
	}
	headroom := func(cpu float64) int {
		if cpu != 20.0 {
			t.Errorf("expected cpu 20.0, got %f", cpu)
		}
		return 4
	}

	result := PassLimit(read, headroom)
	if result != 4 {
		t.Errorf("expected 4, got %d", result)
	}
}

func TestPassLimitNegativeHeadroomClampedToZero(t *testing.T) {
	read := func() (float64, error) {
		return 50.0, nil
	}
	headroom := func(cpu float64) int {
		return -2
	}

	result := PassLimit(read, headroom)
	if result != 0 {
		t.Errorf("expected 0, got %d", result)
	}
}

func TestPassLimitReadErrorIgnoresHeadroom(t *testing.T) {
	readErr := errors.New("cpu read failed")
	read := func() (float64, error) {
		return 0, readErr
	}
	headroomCalled := false
	headroom := func(cpu float64) int {
		headroomCalled = true
		return 6
	}

	result := PassLimit(read, headroom)
	if result != 1 {
		t.Errorf("expected 1, got %d", result)
	}
	if headroomCalled {
		t.Error("headroom should not be called when read fails")
	}
}
