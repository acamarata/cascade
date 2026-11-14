// Package ladder provides order management for ladder rungs.
package ladder

import "errors"

// Rung represents a single step in the ladder order.
type Rung struct {
	Name       string
	Kind       string
	ResetHours int
	Room       bool
	Stale      bool
}

const (
	// KindGC represents the gc kind.
	KindGC = "gc"
	// KindGP represents the gp kind.
	KindGP = "gp"
	// KindCO represents the co kind.
	KindCO = "co"
	// KindClaude represents the claude kind.
	KindClaude = "claude"
	// KindReserved represents the reserved kind.
	KindReserved = "reserved"
)

// ErrNotImplemented indicates that a ladder operation is unavailable.
var ErrNotImplemented = errors.New("ladder: operation unavailable")

// Order processes the given rungs and returns the ordered result.
// It currently returns nil.
func Order(rungs []Rung) []Rung {
	return nil
}
