// Purpose: registers the `completion` root mount.
// SPORT: cmd/cascade root mounts (P1-CORE-01).
package main

import "github.com/spf13/cobra"

var _ = registerRootMount(rootMount{Name: "completion", Order: 30, Mount: func(root *cobra.Command) { root.AddCommand(newCompletionCmd(root)) }})
