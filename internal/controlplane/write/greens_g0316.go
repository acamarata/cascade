package write

import "errors"

// ErrInvalidGreensLifecycle is returned when a GreensLifecycle has invalid or empty fields.
var ErrInvalidGreensLifecycle = errors.New("invalid greens lifecycle")

// GreensState represents the state of a greens lifecycle.
type GreensState string

const (
	// GreensOpen represents an open greens lifecycle state.
	GreensOpen GreensState = "open"
	// GreensReopened represents a reopened greens lifecycle state.
	GreensReopened GreensState = "reopened"
	// GreensClosed represents a closed greens lifecycle state.
	GreensClosed GreensState = "closed"
)

// GreensLifecycle represents a greens lifecycle finding with kind, severity, and state.
type GreensLifecycle struct {
	Kind     string
	Severity string
	State    GreensState
}

// NewGreensLifecycle creates a new GreensLifecycle with default values.
// It returns a GreensLifecycle with Kind="greens_decreased", Severity="error", and State=GreensOpen.
func NewGreensLifecycle() GreensLifecycle {
	return GreensLifecycle{
		Kind:     "greens_decreased",
		Severity: "error",
		State:    GreensOpen,
	}
}

// ValidateGreensLifecycle validates the provided GreensLifecycle.
// It returns the original input and ErrInvalidGreensLifecycle if any field is invalid or empty.
// Valid fields are: Kind="greens_decreased", Severity="error", State is one of GreensOpen, GreensReopened, or GreensClosed.
func ValidateGreensLifecycle(lc GreensLifecycle) (GreensLifecycle, error) {
	if lc.Kind != "greens_decreased" {
		return lc, ErrInvalidGreensLifecycle
	}
	if lc.Severity != "error" {
		return lc, ErrInvalidGreensLifecycle
	}
	switch lc.State {
	case GreensOpen, GreensReopened, GreensClosed:
		return lc, nil
	default:
		return lc, ErrInvalidGreensLifecycle
	}
}
