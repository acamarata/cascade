// Purpose: registers the `sync` root mount.
// SPORT: cmd/cascade root mounts (P1-CORE-01).
package main

var _ = registerRootMount(rootMount{Name: "sync", Order: 230, Mount: mountSyncCmd})
