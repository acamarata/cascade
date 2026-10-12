//go:build !windows

// Purpose: the supervision-directive drain behind context.slice. The
// context-engine registration binds context.slice at phaseNamespaces, before
// any stall detector exists; the stall registration re-binds it here with
// the drainer once the detector's directive store is built, so the drain
// exists exactly when a detector can queue directives. The RPC registry
// replaces a re-registered method, so the method set does not change.
// Inputs: the daemon wiring and daemon.NewLiveDirectiveDrainer's drainer.
// Outputs: context.slice and context.show bound with WithDirectiveDrain.
// Constraints: the live-session and caller rules live in internal/daemon
// (context_assemble_directives.go). The second scope-graph connection follows
// the context-engine registration's documented tradeoff and is not closed
// separately.
// SPORT: cmd/cascade daemon stall supervision (ADDED, P1-SUP-04).
package main

import "github.com/acamarata/cascade/internal/daemon"

// registerContextDrain re-binds context.slice and context.show with the
// directive drain.
func registerContextDrain(w *daemonWiring, drainer daemon.DirectiveDrainer) error {
	_, err := daemon.RegisterContextAssembleHandler(w.Registry, w.Paths, w.Clock, daemon.WithDirectiveDrain(drainer))
	return err
}
