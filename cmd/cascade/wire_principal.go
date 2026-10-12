//go:build !windows

package main

import (
	"github.com/acamarata/cascade/internal/fleet/hookpacks"
	"github.com/acamarata/cascade/internal/fleet/sessions"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/runtime"
)

// Purpose: install caller admission before elevation and every handler.
// Inputs: daemon clock and store. Outputs: process-local resolver and audit rows.
// Constraints: no mint RPC, no token persistence. SPORT: daemon/principal.
var _ = registerDaemonWiring(daemonRegistration{
	Name: "principal", Phase: phaseMiddleware, Order: 5,
	Wire: func(w *daemonWiring) error {
		minter := principal.NewMinter(w.Clock)
		minter.Audit = runtime.NewStoreAuditRecorder(w.Store, w.Clock)
		w.Registry.SetAccessPolicy(func(token string) (principal.Principal, error) {
			return minter.Resolve(token, w.Clock.Now())
		})
		return hookpacks.RegisterHookEventHandler(w.Registry, sessions.New(w.Store, w.Clock, w.Bus), w.Bus, w.Clock)
	},
})
