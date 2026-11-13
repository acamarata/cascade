// Package write implements findings and restarter behaviour for the control plane.
package write

// Restarter is an abstraction over a component that can restart a daemon or
// service. Implementations must not perform any network, CLI or launchd
// operations when used only for constructing findings.
type Restarter interface {
	// Restart attempts to restart the underlying component.
	Restart() error
}

// StaleBinaryFinding returns a Finding describing a stale binary condition.
// The finding states that no automatic restart is performed and that a
// restart requires the owner to name it. The provided Restarter is never
// called; the finding never triggers or schedules a restart.
func StaleBinaryFinding(r Restarter) Finding {
	// Intentionally do not call r.Restart() -- the finding must never
	// trigger or schedule a restart.
	_ = r

	return Finding{
		Kind: "daemon_stale_binary",
		Text: "no automatic restart is performed; a restart requires the owner to name it",
	}
}
