// Purpose: registers the `node` root mount.
// SPORT: cmd/cascade root mounts (P1-CORE-01).
package main

var _ = registerRootMount(rootMount{Name: "node", Order: 220, Mount: mountNodeCmd})
