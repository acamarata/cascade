// Purpose: registers the `memory` root mount.
// SPORT: cmd/cascade root mounts (P1-CORE-01).
package main

var _ = registerRootMount(rootMount{Name: "memory", Order: 120, Mount: mountMemoryCmd})
