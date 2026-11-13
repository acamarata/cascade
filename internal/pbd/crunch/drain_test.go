package crunch

import (
	"testing"
	"time"
)

func TestDrainLowersWidthToZero(t *testing.T) {
	if got := DrainWidth(5); got != 0 {
		t.Errorf("DrainWidth(5) = %d, want 0", got)
	}
	if got := DrainWidth(0); got != 0 {
		t.Errorf("DrainWidth(0) = %d, want 0", got)
	}
}

func TestDrainStopsNewStartsUntilJobsZero(t *testing.T) {
	if AllowNewStart(true) {
		t.Error("AllowNewStart(true) = true, want false while draining")
	}
	if !AllowNewStart(false) {
		t.Error("AllowNewStart(false) = false, want true after drain cleared")
	}
}

func TestDrainAllowsTenMinuteOldJobs(t *testing.T) {
	if !DrainComplete(nil) {
		t.Error("DrainComplete(nil) = false, want true for no jobs")
	}
	if !DrainComplete([]time.Duration{}) {
		t.Error("DrainComplete([]) = false, want true for no jobs")
	}

	// all ages 0..10m complete
	if !DrainComplete([]time.Duration{0, 5 * time.Minute, 10 * time.Minute}) {
		t.Error("DrainComplete([0, 5m, 10m]) = false, want true")
	}

	// 10m1s does not complete
	if DrainComplete([]time.Duration{10*time.Minute + time.Second}) {
		t.Error("DrainComplete([10m1s]) = true, want false")
	}

	// mixed older job does not complete
	if DrainComplete([]time.Duration{5 * time.Minute, 15 * time.Minute}) {
		t.Error("DrainComplete([5m, 15m]) = true, want false")
	}

	// negative ages do not complete
	if DrainComplete([]time.Duration{-time.Second}) {
		t.Error("DrainComplete([-1s]) = true, want false for negative age")
	}
	if DrainComplete([]time.Duration{5 * time.Minute, -time.Minute}) {
		t.Error("DrainComplete([5m, -1m]) = true, want false for mixed negative age")
	}
}

func TestRelaunchOnlyOnEngineFix(t *testing.T) {
	if !AllowRelaunch(true) {
		t.Error("AllowRelaunch(true) = false, want true")
	}
}

func TestNoRelaunchWithoutEngineFix(t *testing.T) {
	if AllowRelaunch(false) {
		t.Error("AllowRelaunch(false) = true, want false")
	}
}
