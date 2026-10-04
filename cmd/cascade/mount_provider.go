// Purpose: registers the `provider` root mount.
// SPORT: cmd/cascade root mounts (P1-CORE-01).
package main

var _ = registerRootMount(rootMount{Name: "provider", Order: 190, Mount: mountProviderCmd})
