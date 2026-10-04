//go:build !windows

// Purpose: registers the review-rpc daemon wiring: plugin.review.review.
// SPORT: cmd/cascade daemon registrations (P1-CORE-01).
package main

var _ = registerDaemonWiring(daemonRegistration{
	Name: "review-rpc", Phase: phaseLate, Order: 30,
	Wire: func(w *daemonWiring) error {
		w.Registry.Register(reviewRPCMethod, reviewRPCHandler) // handler in review_mount.go
		return nil
	},
})
