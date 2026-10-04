// Purpose: registers the `daemon` root mount.
// SPORT: cmd/cascade root mounts (P1-CORE-01).
package main

import "github.com/spf13/cobra"

var _ = registerRootMount(rootMount{Name: "daemon", Order: 50, Mount: func(root *cobra.Command) {
	mountDaemonCmd(root)
	suppressEmbeddedWarning(root, "daemon", "run")
}})
