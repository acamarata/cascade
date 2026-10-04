// Purpose: registers the `config` root mount.
// SPORT: cmd/cascade root mounts (P1-CORE-01).
package main

var _ = registerRootMount(rootMount{Name: "config", Order: 40, Mount: mountConfigCmd})
