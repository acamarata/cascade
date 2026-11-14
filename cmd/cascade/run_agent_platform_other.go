//go:build !windows

// Purpose: the non-Windows twin of the run --agent platform check.
// Outputs: nil; the daemon exists on this platform.
// SPORT: cmd/cascade/run (CHANGE).
package main

// runAgentPlatformCheck allows the command on platforms with a daemon.
func runAgentPlatformCheck() error { return nil }
