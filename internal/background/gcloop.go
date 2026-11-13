package background

import (
	"math"
	"time"
)

// TakeStreamTokens attempts to take n tokens from the bucket at the given time.
// It refills tokens deterministically using discrete one-second ticks.
// Elapsed whole seconds since Last times Rate are added, capped at Capacity
// without integer overflow. Last is advanced by those whole seconds, preserving
// the subsecond remainder. If n is greater than available tokens, it returns
// the allowed tokens and ErrRateLimited.
func TakeStreamTokens(b StreamBucket, n int, now time.Time) (StreamBucket, int, error) {
	if b.Capacity <= 0 {
		return b, 0, ErrInvalidCapacity
	}
	if !b.Initialized || b.Rate <= 0 || b.Tokens < 0 || b.Tokens > b.Capacity || n < 0 {
		return b, 0, ErrInvalidStreamInput
	}
	if now.Before(b.Last) {
		return b, 0, ErrInvalidStreamInput
	}

	elapsed := now.Sub(b.Last)
	seconds := int(elapsed / time.Second)

	tokens := b.Tokens
	if seconds > 0 {
		maxAdd := (math.MaxInt - tokens) / b.Rate
		if seconds > maxAdd {
			tokens = b.Capacity
		} else {
			added := seconds * b.Rate
			tokens += added
			if tokens > b.Capacity {
				tokens = b.Capacity
			}
		}
		b.Last = b.Last.Add(time.Duration(seconds) * time.Second)
	}
	b.Tokens = tokens

	allowed := n
	if allowed > b.Tokens {
		allowed = b.Tokens
	}
	b.Tokens -= allowed

	if allowed < n {
		return b, allowed, ErrRateLimited
	}
	return b, allowed, nil
}
