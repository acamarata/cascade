// Purpose: registers the `fleet` root mount.
// SPORT: cmd/cascade root mounts (P1-CORE-01).
package main

var _ = registerRootMount(rootMount{Name: "fleet", Order: 210, Mount: mountFleetCmd})
