// Purpose: registers the `mcp` root mount.
// SPORT: cmd/cascade root mounts (P1-CORE-01).
package main

var _ = registerRootMount(rootMount{Name: "mcp", Order: 60, Mount: mountMCPCmd})
