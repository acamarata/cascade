// Purpose: registers the `status` root mount.
// SPORT: cmd/cascade root mounts (P1-CORE-01).
package main

import "github.com/spf13/cobra"

var _ = registerRootMount(rootMount{Name: "status", Order: 70, Mount: func(root *cobra.Command) {
	mountStatusCmd(root)
	suppressEmbeddedWarning(root, "status")
}})
