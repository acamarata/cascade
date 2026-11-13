// Package docket provides ticket state validation utilities.
package docket

import "errors"

// ErrD1135 is returned when a running state file access is detected.
var ErrD1135 = errors.New("D1135: running state file access forbidden")

// ValidateNoRunningState inspects planned operations and returns ErrD1135
// if a write or read of a running state file is detected, or if the path is empty.
func ValidateNoRunningState(op string) error {
	if op == "" {
		return ErrD1135
	}
	switch op {
	case "write-running-state", "read-running-state":
		return ErrD1135
	default:
		return nil
	}
}
