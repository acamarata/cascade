package crunch

import (
	"errors"
	"sync"
)

// DailyTicketLimit is the maximum number of daily tickets available.
const DailyTicketLimit = 150

// ErrD2011 indicates that the daily limit has been exceeded.
var ErrD2011 = errors.New("D2011: daily limit exceeded")

// Counter tracks the remaining daily tickets.
// A zero-value Counter is exhausted.
type Counter struct {
	mu        sync.Mutex
	remaining int
}

// NewCounter creates a new Counter with the given number of tickets.
// It clamps n below 0 to 0 and above 150 to 150; otherwise initializes remaining=n.
func NewCounter(n int) *Counter {
	if n < 0 {
		n = 0
	} else if n > DailyTicketLimit {
		n = DailyTicketLimit
	}
	return &Counter{remaining: n}
}

// SpendCeiling is the identity function for every int, including zero/negative
// (validation is caller-owned).
func SpendCeiling(crunchCeiling int) int {
	return crunchCeiling
}

// DecrementAudit decrements the remaining counter by one and returns the new remaining count.
// If the receiver is nil or the remaining count is zero or less, it returns 0 and ErrD2011.
func (c *Counter) DecrementAudit() (int, error) {
	if c == nil {
		return 0, ErrD2011
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.remaining <= 0 {
		return 0, ErrD2011
	}
	c.remaining--
	return c.remaining, nil
}
