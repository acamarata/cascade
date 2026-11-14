// Package monitor provides stall monitoring primitives.
package monitor

import (
	"errors"
	"testing"
	"time"
)

// fakeClock implements Clock for testing.
type fakeClock struct {
	now  time.Time
	fns  []func()
	stop func()
}

func (f *fakeClock) Now() time.Time {
	return f.now
}

func (f *fakeClock) AfterFunc(_ time.Duration, fn func()) func() {
	f.fns = append(f.fns, fn)
	f.stop = func() {
		f.fns = nil
	}
	return f.stop
}

// TestGapOver3IntervalsFlagsIncident verifies that a gap exceeding three intervals
// is flagged as an incident.
func TestGapOver3IntervalsFlagsIncident(t *testing.T) {
	now := time.Date(2024, 1, 1, 0, 0, 31, 0, time.UTC)
	last := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	interval := 10 * time.Second

	incident, err := GapIncident(now, last, interval)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !incident {
		t.Errorf("expected incident=true for gap > 3*interval, got false")
	}
}

// TestGapWithin3IntervalsNoIncident verifies that gaps within three intervals
// (including exactly three) are not flagged.
func TestGapWithin3IntervalsNoIncident(t *testing.T) {
	tests := []struct {
		name string
		gap  time.Duration
	}{
		{"25 seconds", 25 * time.Second},
		{"exactly 30 seconds", 30 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).Add(tt.gap)
			last := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
			interval := 10 * time.Second

			incident, err := GapIncident(now, last, interval)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if incident {
				t.Errorf("expected incident=false for gap <= 3*interval, got true")
			}
		})
	}

	t.Run("zero last means absent", func(t *testing.T) {
		now := time.Date(2024, 1, 1, 0, 0, 100, 0, time.UTC)
		var last time.Time
		interval := 10 * time.Second

		incident, err := GapIncident(now, last, interval)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if incident {
			t.Errorf("expected incident=false for zero last, got true")
		}
	})
}

type invalidInputCase struct {
	name string
	fn   func() error
}

func emitterInvalidCases() []invalidInputCase {
	return []invalidInputCase{
		{
			name: "NewEmitter nil clock",
			fn: func() error {
				_, err := NewEmitter(nil, 10*time.Second, func(Tick) {})
				return err
			},
		},
		{
			name: "NewEmitter nil callback",
			fn: func() error {
				clock := &fakeClock{now: time.Now()}
				_, err := NewEmitter(clock, 10*time.Second, nil)
				return err
			},
		},
		{
			name: "NewEmitter zero interval",
			fn: func() error {
				clock := &fakeClock{now: time.Now()}
				_, err := NewEmitter(clock, 0, func(Tick) {})
				return err
			},
		},
		{
			name: "NewEmitter negative interval",
			fn: func() error {
				clock := &fakeClock{now: time.Now()}
				_, err := NewEmitter(clock, -1*time.Second, func(Tick) {})
				return err
			},
		},
		{
			name: "NewEmitter interval too large",
			fn: func() error {
				clock := &fakeClock{now: time.Now()}
				_, err := NewEmitter(clock, time.Duration(1<<63-1)/2, func(Tick) {})
				return err
			},
		},
	}
}

func gapInvalidCases() []invalidInputCase {
	return []invalidInputCase{
		{
			name: "GapIncident zero now",
			fn: func() error {
				var now time.Time
				last := time.Now()
				_, err := GapIncident(now, last, 10*time.Second)
				return err
			},
		},
		{
			name: "GapIncident zero interval",
			fn: func() error {
				now := time.Now()
				last := now.Add(-10 * time.Second)
				_, err := GapIncident(now, last, 0)
				return err
			},
		},
		{
			name: "GapIncident negative interval",
			fn: func() error {
				now := time.Now()
				last := now.Add(-10 * time.Second)
				_, err := GapIncident(now, last, -1*time.Second)
				return err
			},
		},
		{
			name: "GapIncident last after now",
			fn: func() error {
				now := time.Now()
				last := now.Add(10 * time.Second)
				_, err := GapIncident(now, last, 10*time.Second)
				return err
			},
		},
	}
}

// TestMonitorInvalidInput verifies that invalid inputs are rejected with ErrInvalidMonitorInput.
func TestMonitorInvalidInput(t *testing.T) {
	tests := append(emitterInvalidCases(), gapInvalidCases()...)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.fn()
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !errors.Is(err, ErrInvalidMonitorInput) {
				t.Errorf("expected ErrInvalidMonitorInput, got %v", err)
			}
		})
	}
}
