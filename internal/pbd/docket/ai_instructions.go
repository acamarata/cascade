// Package docket provides project docket utilities.
package docket

import (
	"errors"
	"strings"
)

// ErrAIInstrOverCap is returned when the merged AI instructions exceed the size cap.
var ErrAIInstrOverCap = errors.New("AI_INSTR_OVER_CAP")

// ResolveAIInstructions merges PRC, PAC, and ticket-specific AI instructions in that order,
// separated by a newline. If the merged result exceeds capBytes, it fails closed with ErrAIInstrOverCap.
// An empty ticket string is allowed and yields the inherited tiers only. Empty tiers are skipped.
func ResolveAIInstructions(prc, pac, ticket string, capBytes int) (string, error) {
	var parts []string
	if prc != "" {
		parts = append(parts, prc)
	}
	if pac != "" {
		parts = append(parts, pac)
	}
	if ticket != "" {
		parts = append(parts, ticket)
	}

	merged := strings.Join(parts, "\n")

	if len(merged) > capBytes {
		return "", ErrAIInstrOverCap
	}

	return merged, nil
}
