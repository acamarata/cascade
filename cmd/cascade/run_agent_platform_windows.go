//go:build windows

// Purpose: the Windows refusal for `cascade run --agent`.
// Outputs: KindUnsupported, because the daemon it delegates to does not run on Windows.
// SPORT: cmd/cascade/run (CHANGE).
package main

import "github.com/acamarata/cascade/pkg/cascade"

// runAgentPlatformCheck refuses before any daemon dial.
func runAgentPlatformCheck() error {
	return cascade.New(cascade.KindUnsupported, "cascade run --agent is not available on Windows: the daemon it delegates to does not run there")
}
