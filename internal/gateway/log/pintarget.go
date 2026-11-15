package log

import "errors"

// ErrInvalidTarget is returned when SetTarget receives a negative value.
var ErrInvalidTarget = errors.New("invalid target: must be non-negative")

// PinTarget controls a stream target with optional pinning.
// When pinned, Target returns the pinned value instead of consulting the ramp.
type PinTarget struct {
	ramp    func() int
	pinned  bool
	pinnedV int
}

// NewPinTarget creates a PinTarget with the given ramp function.
// The ramp function is consulted when no pin is active.
func NewPinTarget(ramp func() int) *PinTarget {
	return &PinTarget{
		ramp:   ramp,
		pinned: false,
	}
}

// SetTarget pins the stream target to n when n > 0.
// Setting n to 0 clears the pin and resumes automatic ramp control.
// A negative n returns an error and leaves the state unchanged.
func (p *PinTarget) SetTarget(n int) error {
	if n < 0 {
		return ErrInvalidTarget
	}
	if n == 0 {
		p.pinned = false
		p.pinnedV = 0
		return nil
	}
	p.pinned = true
	p.pinnedV = n
	return nil
}

// Target returns the pinned value if a pin is active,
// otherwise it returns the value from the ramp function.
func (p *PinTarget) Target() int {
	if p.pinned {
		return p.pinnedV
	}
	return p.ramp()
}

// Pinned reports whether a pin is currently active.
func (p *PinTarget) Pinned() bool {
	return p.pinned
}
