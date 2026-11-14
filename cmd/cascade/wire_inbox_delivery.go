//go:build !windows

// Purpose: registers addressed inbox delivery in the daemon composition.
// Inputs: w.Deps.Notify (set by the notify registration, phaseSupervised
// Order 5), w.Store, w.Bus and the run context w.Ctx.
// Outputs: the "inbox-delivery" registration; the inbox.delivery supervised
// loop and the inbox.push Skipped manifest entry (wireInboxDelivery).
// Constraints: the subscriptions live exactly as long as w.Ctx: the loop
// starts through Manifest.GoSupervised and unsubscribes every session when
// the run context ends. The push path stays flag-off.
// SPORT: cmd/cascade inbox delivery wiring.
package main

var _ = registerDaemonWiring(daemonRegistration{
	Name: "inbox-delivery", Phase: phaseSupervised, Order: 9,
	Wire: wireInboxDelivery,
})
