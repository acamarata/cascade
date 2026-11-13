// Package crunch provides priority-based task scheduling with hook support.
package crunch

import (
	"errors"
	"sort"
)

// HookTask represents a schedulable unit with an identifier and priority.
type HookTask struct {
	ID       string
	Priority int
}

// PriorityHook is a function that reorders or validates a slice of tasks.
type PriorityHook func([]HookTask) ([]HookTask, error)

// ErrCodeD1542 is the error code used for invalid hook results.
const ErrCodeD1542 = "D1542"

// HookError represents a domain-specific error with a code and message.
type HookError struct {
	Code string
	Msg  string
}

// Error returns the formatted error string as "Code: Msg".
func (e HookError) Error() string {
	return e.Code + ": " + e.Msg
}

// ErrInvalidHook is returned when hook input or output validation fails.
var ErrInvalidHook = errors.New(ErrCodeD1542 + ": invalid hook result")

// Schedule processes tasks using an optional PriorityHook.
// If the input is empty, contains empty IDs, or has duplicate IDs, it returns ErrInvalidHook.
// A nil hook sorts tasks by descending priority, preserving original order for equal priorities.
// A non-nil hook is called once; its output must contain each original task exactly once unchanged.
func Schedule(tasks []HookTask, hook PriorityHook) ([]HookTask, error) {
	if len(tasks) == 0 {
		return nil, ErrInvalidHook
	}

	seen := make(map[string]bool, len(tasks))
	for _, t := range tasks {
		if t.ID == "" {
			return nil, ErrInvalidHook
		}
		if seen[t.ID] {
			return nil, ErrInvalidHook
		}
		seen[t.ID] = true
	}

	cp := make([]HookTask, len(tasks))
	copy(cp, tasks)

	if hook == nil {
		sort.SliceStable(cp, func(i, j int) bool {
			return cp[i].Priority > cp[j].Priority
		})
		out := make([]HookTask, len(cp))
		copy(out, cp)
		return out, nil
	}

	result, err := hook(cp)
	if err != nil {
		return nil, err
	}

	if len(result) != len(tasks) {
		return nil, ErrInvalidHook
	}

	resSeen := make(map[string]bool, len(result))
	for _, r := range result {
		orig, ok := seen[r.ID]
		_ = orig
		if !ok || resSeen[r.ID] {
			return nil, ErrInvalidHook
		}
		resSeen[r.ID] = true

		var match bool
		for _, t := range tasks {
			if t.ID == r.ID && t.Priority == r.Priority {
				match = true
				break
			}
		}
		if !match {
			return nil, ErrInvalidHook
		}
	}

	out := make([]HookTask, len(result))
	copy(out, result)
	return out, nil
}
