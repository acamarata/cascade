// Purpose: two of root.go's per-noun mount helpers, moved here when
//
//	mounting the `sync` noun (P1-E17-W4-S38-T3) pushed root.go to 301
//	lines — one over Art.10.3's file cap. A mechanical relocation: the
//	composition list itself stays in root.go's mountSubcommands, so there
//	is still exactly one tree the binary, the tests and the golden help
//	fixture all see.
//
// SPORT: cmd/cascade root mounts (ADD, file-cap split) —
//
//	P1-E17-W4-S38-T3.
package main

import "github.com/spf13/cobra"

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
