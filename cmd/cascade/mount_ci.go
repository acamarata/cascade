// Purpose: registers the `ci` root mount and binds the ci tree's attach
// points: deps.WatchEvents is set before ci.NewCICmd (contract:ci-cli-attach).
// SPORT: cmd/cascade root mounts (P1-CORE-01).
package main

import (
	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/ci"
)

var _ = registerRootMount(rootMount{Name: "ci", Order: 280, Mount: mountCIAttachedCmd})

// mountCIAttachedCmd mounts the `ci` tree with its daemon stream bound.
func mountCIAttachedCmd(root *cobra.Command) {
	deps := ci.ProductionCmdDeps()
	deps.WatchEvents = ciWatchEvents(productionStatusDeps())
	cmd := ci.NewCICmd(deps)
	guardUnknownSubcommands(cmd)
	root.AddCommand(cmd)
}
