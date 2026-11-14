// Package consult provides proxy ladder evaluation for sequential rung resolution.
package consult

import (
	"context"
	"errors"
)

// Rung is a function that evaluates a question at a specific escalation level.
// It returns whether the question was resolved and the answer if so.
type Rung func(ctx context.Context, question string) (resolved bool, answer string)

// Result holds the outcome of a successful ladder evaluation.
type Result struct {
	// Rung is the name of the rung that resolved the question.
	Rung string
	// Answer is the response from the resolving rung.
	Answer string
}

// ErrUnresolved is returned when no rung in the ladder resolves the question.
var ErrUnresolved = errors.New("consult: unresolved")

// rungNames defines the fixed order of rungs evaluated by RunLadder.
var rungNames = []string{"PM", "BA", "CEO"}

// RunLadder evaluates a sequence of rungs in fixed order (PM, BA, CEO).
// Evaluation stops at the first resolved rung and later rungs are never called.
// If none resolve, it returns ErrUnresolved.
func RunLadder(ctx context.Context, question string, rungs []Rung) (Result, error) {
	for i, rung := range rungs {
		if i >= len(rungNames) {
			break
		}
		resolved, answer := rung(ctx, question)
		if resolved {
			return Result{
				Rung:   rungNames[i],
				Answer: answer,
			}, nil
		}
	}
	return Result{}, ErrUnresolved
}
