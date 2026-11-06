//go:build !windows

// Purpose: register the owner-only context.generate RPC in the daemon.
// Inputs: daemon paths, clock, store and event bus.
// Outputs: a handler with a persistent supervision sink.
// Constraints: no database opened at wiring time and no MCP tool.
// SPORT: context.generate wiring (P1-GEN-11c).
package main

import (
	"github.com/acamarata/cascade/internal/context/generate"
	"github.com/acamarata/cascade/internal/fleet/supervision"
)

var _ = registerDaemonWiring(daemonRegistration{
	Name: "context-generate", Phase: phaseLate, Order: 60,
	Wire: func(w *daemonWiring) error {
		d := productionContextGenerateDeps(w)
		w.Registry.Register(contextGenerateMethod, d.handle)
		return nil
	},
})

// productionContextGenerateDeps binds generation to the daemon's shared queue.
func productionContextGenerateDeps(w *daemonWiring) contextGenerateDeps {
	var bus supervision.EventBus
	if w.Bus != nil {
		bus = w.Bus
	}
	return contextGenerateDeps{
		Paths: w.Paths, Clock: w.Clock, Probe: generate.NewExecProbe(),
		Attention: supervision.NewStore(w.Store, w.Clock, bus, supervision.NewSystemIDGenerator(), 0),
	}
}
