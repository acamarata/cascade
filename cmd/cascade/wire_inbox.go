//go:build !windows

// Purpose: registers the caller-scoped inbox RPC (inbox.list, inbox.read,
// inbox.ack) over the daemon's one notify.Service.
// Inputs: w.Deps.Notify (set by the notify registration, phaseSupervised
// Order 5) and w.Registry.
// Outputs: the "inbox" registration; the three inbox methods on the daemon
// registry, each with the access class notify declares for it.
// Constraints: no second notify.Service is built here. A missing notify
// runtime is a composition fault and fails the daemon start with
// KindUnavailable. An ack records only that the caller saw an item; it
// never satisfies an approval, a completion gate or any other gate.
package main

import (
	"github.com/acamarata/cascade/internal/notify"
	"github.com/acamarata/cascade/pkg/cascade"
)

var _ = registerDaemonWiring(daemonRegistration{
	Name: "inbox", Phase: phaseSupervised, Order: 7,
	Wire: wireInbox,
})

// wireInbox binds the inbox methods to the daemon's notify.Service.
func wireInbox(w *daemonWiring) error {
	if w.Deps.Notify == nil {
		return cascade.New(cascade.KindUnavailable, "inbox: notify runtime unavailable")
	}
	svc := w.Deps.Notify.Service()
	if svc == nil {
		return cascade.New(cascade.KindUnavailable, "inbox: notify service unavailable")
	}
	return notify.RegisterInboxRPC(w.Registry, svc)
}
