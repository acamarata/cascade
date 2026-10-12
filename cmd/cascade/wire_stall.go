//go:build !windows

// Purpose: registers the stall-escalation daemon wiring (daemon_unix_stall.go).
// SPORT: cmd/cascade daemon registrations (P1-SUP-04).
package main

var _ = registerDaemonWiring(daemonRegistration{Name: "supervision-stall", Phase: phaseSupervised, Order: 50, Wire: wireStallDetector})
