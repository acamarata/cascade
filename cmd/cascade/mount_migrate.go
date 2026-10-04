// Purpose: registers the `migrate` root mount.
// SPORT: cmd/cascade root mounts (P1-CORE-01).
package main

var _ = registerRootMount(rootMount{Name: "migrate", Order: 200, Mount: mountMigrateCmd})
