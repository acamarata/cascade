// Package consult provides consultation filtering utilities.
package consult

import (
	"errors"
	"math"
	"strings"
)

// ErrInvalidAdditionBudget is returned when the token budget is negative.
var ErrInvalidAdditionBudget = errors.New("invalid addition budget")

// ErrInvalidAddition is returned when an addition entry is invalid.
var ErrInvalidAddition = errors.New("invalid addition")

// ErrInvalidRelevance is returned when the minimum relevance threshold is outside [0,1] or not finite.
var ErrInvalidRelevance = errors.New("invalid relevance")

// Addition represents a single consult addition with text content, token cost, and relevance score.
type Addition struct {
	// Text is the content of the addition.
	Text string
	// Tokens is the token cost of the addition.
	Tokens int
	// Relevance is the relevance score of the addition in [0,1].
	Relevance float64
}

// FilterAdditions filters additions by a token budget and minimum relevance threshold.
// It validates all inputs before filtering. Negative budget returns ErrInvalidAdditionBudget.
// Threshold outside inclusive [0,1] or non-finite returns ErrInvalidRelevance.
// Invalid additions (blank text, negative tokens, non-finite or out-of-range relevance,
// or duplicate text) return ErrInvalidAddition. Error precedence is budget, threshold,
// then additions in order. No partial output is ever returned.
func FilterAdditions(adds []Addition, budgetTokens int, minRelevance float64) ([]Addition, error) {
	if budgetTokens < 0 {
		return nil, ErrInvalidAdditionBudget
	}

	if math.IsNaN(minRelevance) || math.IsInf(minRelevance, 0) || minRelevance < 0 || minRelevance > 1 {
		return nil, ErrInvalidRelevance
	}

	seen := make(map[string]bool, len(adds))
	for _, a := range adds {
		if strings.TrimSpace(a.Text) == "" {
			return nil, ErrInvalidAddition
		}
		if a.Tokens < 0 {
			return nil, ErrInvalidAddition
		}
		if math.IsNaN(a.Relevance) || math.IsInf(a.Relevance, 0) || a.Relevance < 0 || a.Relevance > 1 {
			return nil, ErrInvalidAddition
		}
		if seen[a.Text] {
			return nil, ErrInvalidAddition
		}
		seen[a.Text] = true
	}

	if len(adds) == 0 {
		return nil, nil
	}

	result := make([]Addition, 0, len(adds))
	used := 0
	for _, a := range adds {
		if a.Relevance >= minRelevance && used+a.Tokens <= budgetTokens {
			result = append(result, a)
			used += a.Tokens
		}
	}

	return result, nil
}
