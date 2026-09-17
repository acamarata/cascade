//go:build windows

// Purpose: tier 2's Windows answer — a real refusal, not a stub.
//
// WHY THERE IS NO WINDOWS PTY HERE. 06-FORGE-SPEC §2 scopes tier 2 to the
//
//	platforms that have a pseudo-terminal. Windows has ConPTY, which is a
//	different API with different semantics, and shipping a half-working
//	imitation of an approval prompt is worse than saying plainly that this
//	tier is not available: an operator who is told "unsupported" picks
//	another tier, while one whose prompts silently misbehave learns to
//	distrust the approval itself.
//
// Constraints: this file imports no PTY package, so the Windows build
//
//	carries none. It returns a typed refusal that names the two tiers that
//	DO work here, and it never panics and never hangs.
//
// SPORT: fleet.supervision.Tier2Supervisor/ADDED (P1-E18-W4-S39-T3).

package supervision

import "context"

// windowsAttacher refuses every attach.
type windowsAttacher struct{}

// NewPTYAttacher returns the platform's pseudo-terminal attacher. On
// Windows it is one that refuses, so the refusal happens at the first
// attach rather than at the first question.
func NewPTYAttacher() PTYAttacher { return windowsAttacher{} }

// Attach always returns ErrPTYUnavailable and no session.
func (windowsAttacher) Attach(context.Context) (*Session, error) {
	return nil, ErrPTYUnavailable
}
