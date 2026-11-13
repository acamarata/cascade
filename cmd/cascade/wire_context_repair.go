//go:build !windows

// Purpose: mount repair only from the repository-watch composition branch.
package main

func mountContextRepair(w *daemonWiring, set *repoWatchSet) {
	w.Registry.Register(contextRepairMethod, contextRepairHandler(w, set))
}
