// Purpose: registers the `ci` root mount.
// SPORT: cmd/cascade root mounts (P1-CORE-01).
package main

var _ = registerRootMount(rootMount{Name: "ci", Order: 280, Mount: mountCICmd})
