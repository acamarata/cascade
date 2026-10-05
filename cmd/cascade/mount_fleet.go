// Purpose: registers the `fleet` root mount.
// SPORT: cmd/cascade root mounts (P1-CORE-01).
package main

import "github.com/spf13/cobra"

var _ = registerRootMount(rootMount{Name: "fleet", Order: 210, Mount: func(root *cobra.Command) {
	mountFleetCmd(root)
	mountFleetCompletionCheck(root)
}})
