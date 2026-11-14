// Package crunch provides verification utilities for cascade nodes.
package crunch

import "errors"

// ErrVerifyNodesArgs is returned when verify-node arguments are invalid.
// Error code: D2204.
var ErrVerifyNodesArgs = errors.New("D2204: invalid verify-nodes arguments")

// VerifyCollaboratorArgs defines the interface for node verification.
type VerifyCollaboratorArgs interface {
	Verify(commands []string, nodeIDs []string) (string, error)
}

// parseVerifyArgs parses CLI args into a verify boolean.
// Accepts zero args (false), ["--verify-nodes"] or
// ["--verify-nodes=true"] (true), ["--verify-nodes=false"] (false).
// Any other input returns false, ErrVerifyNodesArgs.
func parseVerifyArgs(args []string) (bool, error) {
	if len(args) == 0 {
		return false, nil
	}
	if len(args) > 1 {
		return false, ErrVerifyNodesArgs
	}
	switch args[0] {
	case "--verify-nodes", "--verify-nodes=true":
		return true, nil
	case "--verify-nodes=false":
		return false, nil
	default:
		return false, ErrVerifyNodesArgs
	}
}

// VerifyWithNodesArgs verifies nodes using the provided collaborator.
// It calls parseVerifyArgs before any collaborator call.
// On error it returns a zero target string and ErrVerifyNodesArgs.
func VerifyWithNodesArgs(
	args []string,
	commands []string,
	nodeIDs []string,
	collab VerifyCollaboratorArgs,
) (string, error) {
	want, err := parseVerifyArgs(args)
	if err != nil {
		return "", ErrVerifyNodesArgs
	}
	if !want {
		return "", nil
	}
	if collab == nil {
		return "", ErrVerifyNodesArgs
	}
	if len(commands) == 0 {
		return "", ErrVerifyNodesArgs
	}
	seen := make(map[string]bool)
	for _, c := range commands {
		if c == "" || seen[c] {
			return "", ErrVerifyNodesArgs
		}
		seen[c] = true
	}
	if len(nodeIDs) == 0 {
		return "", ErrVerifyNodesArgs
	}
	nodeSeen := make(map[string]bool)
	for _, n := range nodeIDs {
		if n == "" || nodeSeen[n] {
			return "", ErrVerifyNodesArgs
		}
		nodeSeen[n] = true
	}
	target, err := collab.Verify(commands, nodeIDs)
	if err != nil {
		return "", ErrVerifyNodesArgs
	}
	return target, nil
}
