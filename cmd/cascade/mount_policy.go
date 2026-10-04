// Purpose: registers the `policy` root mount.
// SPORT: cmd/cascade root mounts (P1-CORE-01).
package main

var _ = registerRootMount(rootMount{Name: "policy", Order: 180, Mount: mountPolicyCmd})
