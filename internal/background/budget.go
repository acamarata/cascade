// Package background provides budget tracking for rollout and improvement operations.
package background

import "errors"

// MaxRollouts is the maximum number of rollouts allowed per request.
const MaxRollouts = 4000

// MaxImprovements is the maximum number of improvements allowed per request.
const MaxImprovements = 3000

// ErrOverage is returned when a request exceeds the allowed budget.
var ErrOverage = errors.New("budget overage")

// ErrInvalidBudget is returned when a request contains invalid parameters or the receiver is nil.
var ErrInvalidBudget = errors.New("invalid budget")

// Request represents a budget admission request.
type Request struct {
	// Rollouts is the number of rollouts requested.
	Rollouts int
	// Improvements is the number of improvements requested.
	Improvements int
	// Parallel indicates whether the request is for parallel execution.
	Parallel bool
}

// Verdict represents the outcome of a budget admission check.
type Verdict string

// VerdictAccepted indicates the request was accepted.
const VerdictAccepted Verdict = "accepted"

// VerdictRejected indicates the request was rejected.
const VerdictRejected Verdict = "rejected"

// Ledger tracks budget admissions. It is stateless and never accumulates across calls.
type Ledger struct{}

// NewLedger creates a new Ledger instance.
func NewLedger() *Ledger {
	return &Ledger{}
}

// Admit evaluates a Request against the budget limits.
// It is stateless and has no partial effects.
// Counts from zero through the inclusive maxima are valid.
// Any negative count or nil receiver returns VerdictRejected, ErrInvalidBudget.
// A zero request is valid and returns VerdictAccepted, nil.
func (l *Ledger) Admit(r Request) (Verdict, error) {
	if l == nil {
		return VerdictRejected, ErrInvalidBudget
	}

	if r.Rollouts < 0 || r.Improvements < 0 {
		return VerdictRejected, ErrInvalidBudget
	}

	if r.Rollouts > MaxRollouts || r.Improvements > MaxImprovements {
		return VerdictRejected, ErrOverage
	}

	return VerdictAccepted, nil
}
