// Purpose: the root mount iteration (mountSubcommands) and the `mcp` and
//
//	`daemon` mount helpers. mountSubcommands moved here from root.go
//	(S-178) and now only iterates the registrations in mount_<noun>.go.
//
// SPORT: cmd/cascade root mounts (P1-E17-W4-S38-T3, P1-CORE-01).
package main

import (
	"sort"

	"github.com/spf13/cobra"
)

// mountMCPCmd attaches the `mcp` command tree (D/S-06.T6), following
// mountDaemonCmd's exact pattern.
func mountMCPCmd(root *cobra.Command) {
	cmd := newMCPCmd(productionMCPDeps())
	guardUnknownSubcommands(cmd)
	root.AddCommand(cmd)
}

// mountDaemonCmd attaches the `daemon` command tree (D/S-06.T2), following
// mountConfigCmd's exact pattern: cmd/cascade/daemon.go's newDaemonCmd is
// package-local (daemon.go lives in package main, unlike config's
// subpackage), so this is a direct call rather than an import, but the
// deferred-environment-resolution and guardUnknownSubcommands treatment are
// identical.
func mountDaemonCmd(root *cobra.Command) {
	cmd := newDaemonCmd(productionDaemonDeps())
	guardUnknownSubcommands(cmd)
	root.AddCommand(cmd)
}

// mountSubcommands attaches every registered command group to root, sorted by
// (Order, Name). The registrations (mount_<noun>.go) are the composition; this
// function holds no noun and is the only place that iterates them, so there is
// exactly one tree the binary, the tests and the golden help all see.
func mountSubcommands(root *cobra.Command) {
	mounts := append([]rootMount(nil), rootMounts...)
	sort.SliceStable(mounts, func(i, j int) bool {
		if mounts[i].Order != mounts[j].Order {
			return mounts[i].Order < mounts[j].Order
		}
		return mounts[i].Name < mounts[j].Name
	})
	for _, m := range mounts {
		m.Mount(root)
	}
}
