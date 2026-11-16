//go:build !windows

// Purpose: registers the conductor daemon wiring: conductor.execute and jobs.reachability.
// SPORT: cmd/cascade daemon registrations (P1-CORE-01).
package main

var _ = registerDaemonWiring(daemonRegistration{
	Name: "conductor", Phase: phaseConductor, Order: 10,
	Wire: func(w *daemonWiring) error {
		permits, err := restrictedPermitsOption(w.Deps.Config)
		if err != nil {
			return err
		}
		values, err := restrictedPermitsOf(w.Deps.Config)
		if err != nil {
			return err
		}
		capOpt := laneTierCapOption(fleetTopology(w.Deps.Fleet), values)
		fanOut, err := wireConductorAndReachability(w.Ctx, w.Registry, w.Manifest, w.Paths, w.Clock, w.Store, nodeTunnelLookup(w.Opts), permits, capOpt)
		w.ConductorFanOut = fanOut
		if err != nil || w.Store == nil {
			return err // no store: no conductor and no evidence rule (paid stays public)
		}
		// The paid-evidence rule the lane tier cap and the lease floor read,
		// keyed over the inputs the conductor's resolver handed to the domain promote.
		return bindPaidEvidence(w.Paths)
	},
})
