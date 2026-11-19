// Package background provides background agent services and safety guards.
package background

import "errors"

// ErrM1478 indicates that a force-push was detected or a pre-push hook failed.
var ErrM1478 = errors.New("M1478: force-push rejected or pre-push hook failed")

// HookRunner executes a pre-push hook and returns any error encountered.
type HookRunner func(args []string) error

// Guard inspects git command arguments and enforces policy for agent git clients.
type Guard struct {
	// PrePushHook is executed before allowing a push command to proceed.
	PrePushHook HookRunner
}

// NewGuard constructs a Guard with the provided pre-push hook runner.
func NewGuard(runner HookRunner) *Guard {
	return &Guard{PrePushHook: runner}
}

// Evaluate validates the given git command arguments.
// It returns ErrM1478 if a force-push flag is present or if the pre-push hook fails.
func (g *Guard) Evaluate(args []string) error {
	for _, arg := range args {
		if arg == "--force" || arg == "--force-with-lease" {
			return ErrM1478
		}
	}
	if len(args) > 0 && args[0] == "push" && g.PrePushHook != nil {
		if err := g.PrePushHook(args); err != nil {
			return ErrM1478
		}
	}
	return nil
}
