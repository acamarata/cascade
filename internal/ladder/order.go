// Package ladder provides order management for ladder rungs.
package ladder

// Rung represents a single step in the ladder order.
type Rung struct {
	Name       string
	Kind       string
	ResetHours int
	Room       bool
	Stale      bool
}

const (
	// KindPrimary is the primary provider kind.
	KindPrimary = "primary"
	// KindSecondary is the secondary provider kind.
	KindSecondary = "secondary"
	// KindTertiary is the tertiary provider kind.
	KindTertiary = "tertiary"
	// KindWindowed is the kind for accounts with a reset window.
	KindWindowed = "windowed"
	// KindReserved represents the reserved kind.
	KindReserved = "reserved"
)
