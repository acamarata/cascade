// Package limiter provides a token-bucket rate limiter with an injectable clock.
package limiter

import (
	"context"
	"errors"
	"math"
	"reflect"
	"sync"
	"time"
)

// ErrInvalidArgument is returned when invalid arguments are provided.
var ErrInvalidArgument = errors.New("invalid argument")

// Clock is the interface for time operations used by the Limiter.
type Clock interface {
	// Now returns the current time.
	Now() time.Time
	// NewTimer creates a new Timer that fires after duration d.
	NewTimer(d time.Duration) Timer
}

// Timer represents a single event timer.
type Timer interface {
	// C returns the channel on which the timer fires.
	C() <-chan time.Time
	// Stop prevents the timer from firing. It returns true if the call stops the timer.
	Stop() bool
}

// Limiter controls the rate of operations using a token bucket algorithm.
type Limiter struct {
	mu          sync.Mutex
	clock       Clock
	rate        int
	capacity    float64
	tokens      float64
	last        time.Time
	lastHalving time.Time
}

// New creates a new Limiter with the given requests-per-minute rate and clock.
// It returns nil, ErrInvalidArgument if rpm <= 0 or clock is nil or a typed nil.
func New(rpm int, clock Clock) (*Limiter, error) {
	if rpm <= 0 || clock == nil || isNilClock(clock) {
		return nil, ErrInvalidArgument
	}
	now := clock.Now()
	return &Limiter{
		clock:    clock,
		rate:     rpm,
		capacity: float64(rpm),
		tokens:   float64(rpm),
		last:     now,
	}, nil
}

func isNilClock(c Clock) bool {
	v := reflect.ValueOf(c)
	k := v.Kind()
	if k == reflect.Pointer || k == reflect.Interface {
		return v.IsNil()
	}
	return false
}

// Rate returns the configured rate in requests per minute.
func (l *Limiter) Rate() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.refill()
	return l.rate
}

// Wait blocks until a token is available or the context is cancelled.
// A nil ctx returns ErrInvalidArgument without consuming a token.
func (l *Limiter) Wait(ctx context.Context) error {
	if ctx == nil {
		return ErrInvalidArgument
	}

	for {
		l.mu.Lock()

		if err := ctx.Err(); err != nil {
			l.mu.Unlock()
			return err
		}

		l.refill()

		if l.tokens >= 1.0 {
			l.tokens -= 1.0
			l.mu.Unlock()
			return nil
		}

		deficit := 1.0 - l.tokens
		waitSec := deficit * 60.0 / float64(l.rate)
		waitNs := int64(math.Ceil(waitSec * 1e9))
		waitDur := time.Duration(waitNs) * time.Nanosecond

		timer := l.clock.NewTimer(waitDur)
		tch := timer.C()
		l.mu.Unlock()

		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-tch:
			timer.Stop()
		}
	}
}

func (l *Limiter) refill() {
	now := l.clock.Now()
	elapsed := now.Sub(l.last)
	if elapsed > 0 {
		added := elapsed.Seconds() * float64(l.rate) / 60.0
		l.tokens += added
		if l.tokens > l.capacity {
			l.tokens = l.capacity
		}
		l.last = now
	}
}

// OnOverload handles overload events by halving the current rate, subject to a 30-second debounce.
// It first materializes any elapsed recovery/refill, then applies halving/debounce.
// Every overload resets the recovery quiet-period anchor.
func (l *Limiter) OnOverload() {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.refill()

	now := l.clock.Now()

	if l.lastHalving.IsZero() || now.Sub(l.lastHalving) >= 30*time.Second {
		l.rate = l.rate / 2
		if l.rate < 1 {
			l.rate = 1
		}
		l.capacity = float64(l.rate)
		if l.tokens > l.capacity {
			l.tokens = l.capacity
		}
	}
	l.lastHalving = now
}
