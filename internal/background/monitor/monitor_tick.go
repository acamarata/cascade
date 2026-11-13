// Package monitor provides stall monitoring primitives.
package monitor

import (
	"errors"
	"time"
)

// ErrInvalidMonitorInput is returned when constructor or function arguments are invalid.
var ErrInvalidMonitorInput = errors.New("invalid monitor input")

// Clock abstracts time operations for testing.
type Clock interface {
	// Now returns the current time.
	Now() time.Time
	// AfterFunc waits for duration d then calls f, returning a stop function.
	AfterFunc(d time.Duration, f func()) (stop func())
}

// Tick represents a single emitted tick event.
type Tick struct {
	// Seq is the sequence number of this tick.
	Seq uint64
	// At is the time at which the tick was emitted.
	At time.Time
}

// Emitter emits ticks at a configured interval using a Clock.
type Emitter struct {
	clock    Clock
	interval time.Duration
	emit     func(Tick)
	seq      uint64
	stopFn   func()
	stopped  bool
}

// NewEmitter creates a dormant Emitter that will emit ticks at the given interval.
// It rejects nil clock or callback, and intervals <= 0 or > maxDuration/3.
func NewEmitter(c Clock, interval time.Duration, emit func(Tick)) (*Emitter, error) {
	if c == nil || emit == nil {
		return nil, ErrInvalidMonitorInput
	}
	maxInterval := time.Duration(1<<63-1) / 3
	if interval <= 0 || interval > maxInterval {
		return nil, ErrInvalidMonitorInput
	}
	return &Emitter{
		clock:    c,
		interval: interval,
		emit:     emit,
	}, nil
}

// Start begins emitting ticks. Returns ErrInvalidMonitorInput if already started
// or previously stopped.
func (e *Emitter) Start() error {
	if e.stopFn != nil || e.stopped {
		return ErrInvalidMonitorInput
	}
	e.schedule()
	return nil
}

func (e *Emitter) schedule() {
	e.stopFn = e.clock.AfterFunc(e.interval, func() {
		e.seq++
		e.emit(Tick{Seq: e.seq, At: e.clock.Now()})
		e.schedule()
	})
}

// Stop halts the emitter. It is idempotent.
func (e *Emitter) Stop() {
	if e.stopFn != nil {
		e.stopFn()
		e.stopFn = nil
	}
	e.stopped = true
}

// GapIncident reports whether the gap between now and last exceeds three intervals.
// A zero last means no tick has been received yet and returns false, nil.
// Rejects zero now, invalid interval, or last after now.
func GapIncident(now, last time.Time, interval time.Duration) (bool, error) {
	if now.IsZero() {
		return false, ErrInvalidMonitorInput
	}
	maxInterval := time.Duration(1<<63-1) / 3
	if interval <= 0 || interval > maxInterval {
		return false, ErrInvalidMonitorInput
	}
	if last.IsZero() {
		return false, nil
	}
	if last.After(now) {
		return false, ErrInvalidMonitorInput
	}
	return now.Sub(last) > 3*interval, nil
}
