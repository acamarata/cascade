// Purpose: registers the `vault` root mount.
// SPORT: cmd/cascade root mounts (P1-CORE-01).
package main

var _ = registerRootMount(rootMount{Name: "vault", Order: 100, Mount: mountVaultCmd})
