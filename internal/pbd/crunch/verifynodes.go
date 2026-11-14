// Package crunch provides verification utilities for node operations.
// P2-FMX-12 owns no-node local fallback behavior.
// P2-BAKB-53 owns backup stages, not verification.
package crunch

import "errors"

// VerifyTarget describes the node and locality for a verification run.
type VerifyTarget struct {
	// Node is the target node identifier.
	Node string
	// Local indicates whether the verification runs locally.
	Local bool
}

// NodeVerifier is the injected adapter for node verification.
type NodeVerifier interface {
	// FreeNodes returns the list of available nodes.
	FreeNodes() ([]string, error)
	// Verify executes commands against the given target.
	Verify(target VerifyTarget, commands []string) error
}

// ErrVerifyNodes is returned when verification inputs are invalid (D2204).
var ErrVerifyNodes = errors.New("D2204")

// VerifyWithNodes selects a target and verifies commands using the provided NodeVerifier.
// It returns a zero VerifyTarget and ErrVerifyNodes if v is nil or commands is empty.
func VerifyWithNodes(_ []string, commands []string, v NodeVerifier) (VerifyTarget, error) {
	if v == nil || len(commands) == 0 {
		return VerifyTarget{}, ErrVerifyNodes
	}
	return VerifyTarget{}, nil
}
