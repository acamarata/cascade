// Purpose: registers the `recall` root mount.
// SPORT: cmd/cascade root mounts (P1-CORE-01).
package main

var _ = registerRootMount(rootMount{Name: "recall", Order: 130, Mount: mountRecallCmd})
