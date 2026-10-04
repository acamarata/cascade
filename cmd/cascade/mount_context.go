// Purpose: registers the `context` root mount.
// SPORT: cmd/cascade root mounts (P1-CORE-01).
package main

var _ = registerRootMount(rootMount{Name: "context", Order: 150, Mount: mountContextCmd})
