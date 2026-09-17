//go:build !darwin && !linux && !windows

// Purpose: tier 2 on a platform nobody has tested it on.
//
// The refusal is the same shape as the Windows one and for a stronger
// reason: an untested pseudo-terminal path is not a capability, it is a
// guess. An operator here is told to pick tier 1 or tier 3, which both
// work everywhere.
//
// SPORT: fleet.supervision.Tier2Supervisor/ADDED (P1-E18-W4-S39-T3).

package supervision

import "context"

// untestedAttacher refuses every attach.
type untestedAttacher struct{}

// NewPTYAttacher returns the platform's pseudo-terminal attacher.
func NewPTYAttacher() PTYAttacher { return untestedAttacher{} }

// Attach always returns ErrPTYUnavailable and no session.
func (untestedAttacher) Attach(context.Context) (*Session, error) {
	return nil, ErrPTYUnavailable
}
