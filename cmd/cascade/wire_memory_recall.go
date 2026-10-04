//go:build !windows

// Purpose: registers the memory-recall daemon wiring: memory.* and recall.*.
// SPORT: cmd/cascade daemon registrations (P1-CORE-01).
package main

var _ = registerDaemonWiring(daemonRegistration{
	Name: "memory-recall", Phase: phaseNamespaces, Order: 10,
	Wire: func(w *daemonWiring) error {
		return registerMemoryAndRecall(w.Registry, w.Paths, w.Clock, w.Bus, w.Store, w.MemoryAdmin)
	},
})
