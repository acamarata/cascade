package broker

import (
	"errors"
	"testing"
)

func TestCapacityLimitRPM(t *testing.T) {
	got, err := CapacityLimit("lane-default")
	if err != nil {
		t.Fatalf("CapacityLimit(lane-default) returned error: %v", err)
	}
	if got.RPM != 60 {
		t.Errorf("CapacityLimit(lane-default).RPM = %d, want 60", got.RPM)
	}

	unknown, err := CapacityLimit("lane-unknown")
	if err == nil {
		t.Fatal("CapacityLimit(lane-unknown) returned nil error, want ErrInvalidLane")
	}
	if unknown != (Limit{}) {
		t.Errorf("CapacityLimit(lane-unknown) = %+v, want zero Limit", unknown)
	}
	if !errors.Is(err, ErrInvalidLane) {
		t.Errorf("errors.Is(CapacityLimit(lane-unknown) error, ErrInvalidLane) = false, want true")
	}
	if want := "D3813: invalid lane"; err.Error() != want {
		t.Errorf("CapacityLimit(lane-unknown) error = %q, want %q", err.Error(), want)
	}
}

func TestCapacityLimitInFlight(t *testing.T) {
	got, err := CapacityLimit("lane-default")
	if err != nil {
		t.Fatalf("CapacityLimit(lane-default) returned error: %v", err)
	}
	if got.InFlight != 10 {
		t.Errorf("CapacityLimit(lane-default).InFlight = %d, want 10", got.InFlight)
	}

	empty, err := CapacityLimit("")
	if err == nil {
		t.Fatal("CapacityLimit(\"\") returned nil error, want ErrInvalidLane")
	}
	if empty != (Limit{}) {
		t.Errorf("CapacityLimit(\"\") = %+v, want zero Limit", empty)
	}
	if !errors.Is(err, ErrInvalidLane) {
		t.Errorf("errors.Is(CapacityLimit(\"\") error, ErrInvalidLane) = false, want true")
	}
}
