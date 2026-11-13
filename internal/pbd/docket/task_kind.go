// Package docket defines task kinds and validation for docket rows.
package docket

import "fmt"

// TaskKind identifies the category of a task row.
type TaskKind string

const (
	// KindAgent represents an agent task.
	KindAgent TaskKind = "agent"
	// KindModel represents a model task.
	KindModel TaskKind = "model"
	// KindService represents a service task.
	KindService TaskKind = "service"
	// KindCI represents a CI task.
	KindCI TaskKind = "ci"
	// KindCam represents a cam task.
	KindCam TaskKind = "cam"
	// KindBuild represents a build task.
	KindBuild TaskKind = "build"
	// KindNode represents a node task.
	KindNode TaskKind = "node"
	// KindScript represents a script task.
	KindScript TaskKind = "script"
	// KindShell represents a shell task.
	KindShell TaskKind = "shell"
)

// validKinds holds every accepted TaskKind value.
var validKinds = map[TaskKind]bool{
	KindAgent:   true,
	KindModel:   true,
	KindService: true,
	KindCI:      true,
	KindCam:     true,
	KindBuild:   true,
	KindNode:    true,
	KindScript:  true,
	KindShell:   true,
}

// TaskRow carries the metadata required to validate a single docket entry.
type TaskRow struct {
	// Kind is the task category.
	Kind TaskKind
	// Run identifies the run this row belongs to.
	Run string
	// Account identifies the account owning this row.
	Account string
	// Headless marks whether the task runs without a UI. Nil means inherit from parent.
	Headless *bool
}

// ValidateTaskRow checks that row has a known kind and non-empty run and account.
// When row.Headless is nil and parent is non-nil, the headless flag is inherited.
func ValidateTaskRow(row TaskRow, parent *TaskRow) (TaskRow, error) {
	if !validKinds[row.Kind] {
		return row, fmt.Errorf("unknown task kind %q", string(row.Kind))
	}
	if row.Run == "" {
		return row, fmt.Errorf("missing run")
	}
	if row.Account == "" {
		return row, fmt.Errorf("missing account")
	}
	if row.Headless == nil && parent != nil && parent.Headless != nil {
		v := *parent.Headless
		row.Headless = &v
	}
	return row, nil
}
