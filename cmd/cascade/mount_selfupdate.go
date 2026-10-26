// Purpose: registers the `self-update` root mount.
// SPORT: cmd/cascade root mounts (P1-CORE-01).
package main

import "github.com/spf13/cobra"

var _ = registerRootMount(rootMount{Name: "self-update", Order: 262, Mount: func(root *cobra.Command) {
	root.AddCommand(newSelfUpdateCmd())
	suppressEmbeddedWarning(root, "self-update")
}})
