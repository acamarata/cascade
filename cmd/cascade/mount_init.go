// Purpose: registers the `init` root mount.
// SPORT: cmd/cascade root mounts (P1-CORE-01).
package main

var _ = registerRootMount(rootMount{Name: "init", Order: 10, Mount: mountInitCmd})
