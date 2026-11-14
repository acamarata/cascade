// Purpose: registers the `inbox` root mount.
// SPORT: cmd/cascade root mounts (P1-CORE-01).
package main

var _ = registerRootMount(rootMount{Name: "inbox", Order: 269, Mount: mountInboxCmd})
