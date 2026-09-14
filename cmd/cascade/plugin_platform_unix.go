//go:build !windows

// Purpose: the POSIX half of the plugin CLI's platform gate. On a platform
// where the daemon and its elevation flow both exist, nothing is refused
// locally — the daemon-required and CASCADE_NO_INPUT checks decide.
//
// SPORT: cli/plugin-lifecycle/ADD (P1-E15-W4-S32-T4).
package main

// pluginWindowsTier2Refusal returns nil on POSIX.
func pluginWindowsTier2Refusal(string) error { return nil }
