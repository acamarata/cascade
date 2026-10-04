//go:build !windows

// Purpose: registers the options daemon wiring: the caller's rpcServerOption registrations (policy, status widget).
// SPORT: cmd/cascade daemon registrations (P1-CORE-01).
package main

var _ = registerDaemonWiring(daemonRegistration{
	Name: "options", Phase: phaseOptions, Order: 10,
	Wire: func(w *daemonWiring) error {
		return applyServerOptions(w.Registry, w.Opts)
	},
})
