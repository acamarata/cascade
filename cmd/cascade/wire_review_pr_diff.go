//go:build !windows

// Purpose: registers the daemon's plugin.review.pr_diff verb, so a
//
//	`review --pr` CLI fetches its diff through the daemon's own plugin
//	launch environment while the daemon holds the runtime store.
//
// Inputs: the daemon wiring's store, clock and registry.
// Outputs: the "plugin-review-pr-diff" registration; the owner-only access
//
//	declaration.
//
// Constraints: owner-only; registered only over a daemon store, so the
//
//	launch environment is the one wire_plugin_launch.go installed over it
//	(phaseCore, before this phase); no second fetch path.
//
// SPORT: cmd/cascade daemon registrations — plugin.review.pr_diff (P1-PLG-14, P1-BF-R305).
package main

import (
	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/internal/rpc"
)

var _ = rpc.DeclareAccess(reviewPRDiffMethod, rpc.AccessOwnerOnly)

var _ = registerDaemonWiring(daemonRegistration{
	Name: "plugin-review-pr-diff", Phase: phaseNamespaces, Order: 45,
	Wire: func(w *daemonWiring) error {
		if w.Store == nil {
			return nil
		}
		w.Registry.Register(reviewPRDiffMethod, reviewPRDiffHandler(plugins.InProcessPRDiffFetcher(), w.Clock))
		return nil
	},
})
