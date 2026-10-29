//go:build !windows

// Purpose: registers away mode (daemon_away.go).
// SPORT: cmd/cascade daemon registrations — away mode (P1-HRN-11).
package main

// Order 60 follows the notify runtime (Order 5) and the stall detector
// (Order 50) in phaseSupervised and sits after the presence middleware
// (phaseMiddleware) and the fleet runtime composeDaemon builds first.
var _ = registerDaemonWiring(daemonRegistration{Name: "away", Phase: phaseSupervised, Order: 60, Wire: wireAwayMode})
