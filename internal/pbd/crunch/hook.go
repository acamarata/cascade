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
	seen, ok := indexTasks(tasks)
	if !ok {
		return nil, ErrInvalidHook
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

	if !validResult(tasks, result, seen) {
		return nil, ErrInvalidHook
	}

	out := make([]HookTask, len(result))
	copy(out, result)
	return out, nil
}

// indexTasks returns the set of task IDs and false if the input is empty,
// has an empty ID or has a duplicate ID.
func indexTasks(tasks []HookTask) (map[string]bool, bool) {
	if len(tasks) == 0 {
		return nil, false
	}
	seen := make(map[string]bool, len(tasks))
	for _, t := range tasks {
		if t.ID == "" || seen[t.ID] {
			return nil, false
		}
		seen[t.ID] = true
	}
	return seen, true
}

// validResult reports whether result holds each original task exactly once, unchanged.
func validResult(tasks, result []HookTask, seen map[string]bool) bool {
	if len(result) != len(tasks) {
		return false
	}
	resSeen := make(map[string]bool, len(result))
	for _, r := range result {
		if !seen[r.ID] || resSeen[r.ID] {
			return false
		}
		resSeen[r.ID] = true
		if !containsTask(tasks, r) {
			return false
		}
	}
	return true
}

// containsTask reports whether tasks has an entry with the same ID and priority as r.
func containsTask(tasks []HookTask, r HookTask) bool {
	for _, t := range tasks {
		if t.ID == r.ID && t.Priority == r.Priority {
			return true
		}
	}
	return false
}
