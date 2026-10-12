//go:build !windows

// Purpose: registers the egress-firewall daemon wiring: the process firewall
// (custody, vault, engine factory) and the process detector are built right
// after status.get and before any plugin, conductor or transport wiring, so
// a daemon that cannot build them refuses to start, naming this entry.
// SPORT: cmd/cascade daemon registrations (egress firewall).
package main

import (
	"fmt"
	"log/slog"

	"github.com/acamarata/cascade/internal/secrets"
)

// egressFirewallSubsystem is the manifest entry the wiring records: the
// detector settings in force, so status.get shows which [secrets] rules the
// daemon scans under.
const egressFirewallSubsystem = "egress-firewall"

var _ = registerDaemonWiring(daemonRegistration{
	Name: egressFirewallSubsystem, Phase: phaseCore, Order: 15,
	Wire: wireEgressFirewall,
})

// wireEgressFirewall builds the process firewall over the daemon's paths and
// resolves the process detector. Either failure stops the daemon. The
// daemon owns the binding: the process firewall is (re)bound to its data
// directory, and a later caller naming another directory is refused.
func wireEgressFirewall(w *daemonWiring) error {
	if _, err := bindDaemonFirewall(w.Paths); err != nil {
		return err
	}
	if _, err := secrets.ProcessDetector(); err != nil {
		return err
	}
	cfg := secrets.ProcessDetectionConfig()
	if w.Manifest != nil {
		w.Manifest.Started(egressFirewallSubsystem, fmt.Sprintf("confidence_threshold=%g entropy_floor=%g",
			cfg.ConfidenceThreshold, cfg.EntropyFloor))
	}
	if w.Logger != nil {
		w.Logger.Info("egress firewall ready",
			slog.Float64("confidence_threshold", cfg.ConfidenceThreshold),
			slog.Float64("entropy_floor", cfg.EntropyFloor))
	}
	return nil
}
