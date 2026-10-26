//go:build !linux

package agents

// Purpose: HideDaemonEnviron off linux is a no-op. darwin has no per-process
// switch that hides the environment from a same-uid KERN_PROCARGS2 reader; that
// is disclosed in docs/security-posture/agent-owner-reach.md
// (DEF-P2-agent-owner-reach), not claimed closed.

// HideDaemonEnviron does nothing and returns nil on this platform.
func HideDaemonEnviron() error { return nil }
