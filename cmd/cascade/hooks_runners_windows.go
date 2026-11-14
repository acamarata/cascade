//go:build windows

// Purpose: the Windows hook runner stub: no action type has a runner there.
// Inputs: an action type from the [[hooks]] section parser.
// Outputs: hookRunnable, always false, so the section refuses every entry.
// Constraints: hook runners are not built for Windows.

package main

import "github.com/acamarata/cascade/internal/hooks"

// hookRunnable reports whether t has a registered runner: never on Windows.
func hookRunnable(hooks.ActionType) bool { return false }
