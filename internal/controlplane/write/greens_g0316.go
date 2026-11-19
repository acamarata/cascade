package write

import "errors"

// ErrInvalidGreensLifecycle is returned when a GreensLifecycle has invalid or empty fields.
var ErrInvalidGreensLifecycle = errors.New("invalid greens lifecycle")

// ErrGreensDecreased is the sentinel error returned when a greens finding is unresolved without a CLI rebase.
var ErrGreensDecreased = errors.New("D1089")

// TickGreens processes a greens lifecycle finding for a single tick.
// It validates the input; if invalid or empty, it returns the original value and ErrInvalidGreensLifecycle without mutation.
// When cliRebase is false and the state is open or reopened, it returns State=GreensReopened, Severity="error", and ErrGreensDecreased on every tick.
// The input value is never mutated.
func TickGreens(f GreensLifecycle, cliRebase bool) (GreensLifecycle, error) {
	v, err := ValidateGreensLifecycle(f)
	if err != nil {
		return v, err
	}

	if !cliRebase {
		if f.State == GreensOpen || f.State == GreensReopened {
			out := f
			out.State = GreensReopened
			out.Severity = "error"
			return out, ErrGreensDecreased
		}
	}

	return f, nil
}

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
