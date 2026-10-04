// Purpose: registers the `run` root mount.
// SPORT: cmd/cascade root mounts (P1-CORE-01).
package main

import "github.com/spf13/cobra"

var _ = registerRootMount(rootMount{Name: "run", Order: 260, Mount: func(root *cobra.Command) {
	mountRunCmd(root)
	suppressEmbeddedWarning(root, "run")
}})
