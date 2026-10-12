//go:build !windows

package main

import (
	"github.com/acamarata/cascade/internal/fleet/hookpacks"
	"github.com/acamarata/cascade/internal/fleet/sessions"
)

// Purpose: confine every caller principal to its own session subtree.
// Inputs: daemon store, clock and bus. Outputs: the registry's event-visibility
// factory (fleet.sessions stream) and the session-binding middleware (list,
// attention and hook_event). Constraints: ancestry comes only
// from the sessions store, at every depth; params are never authority.
// SPORT: daemon/session-scope.
var _ = registerDaemonWiring(daemonRegistration{
	Name: "session-scope", Phase: phaseCore, Order: 20,
	Wire: wireSessionScope,
})

// sessionBinding names the session-addressed verbs a principal is bound to.
var sessionBinding = sessions.SessionBinding{
	Methods:     []string{hookpacks.MethodHookEvent},
	Creator:     hookpacks.MethodHookEvent,
	StartEvents: []string{string(hookpacks.EventSessionStart), string(hookpacks.EventSubagentStart)},
}

// wireSessionScope installs one store-backed ancestry predicate for both the
// event stream and the RPC dispatch path.
func wireSessionScope(w *daemonWiring) error {
	store := sessions.New(w.Store, w.Clock, w.Bus)
	w.Registry.SetEventVisibleFactory(store.Visibility)
	w.Registry.Use(sessions.ScopeMiddleware(store, sessionBinding))
	return nil
}
