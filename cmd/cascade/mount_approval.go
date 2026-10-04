// Purpose: registers the `approval` root mount.
// SPORT: cmd/cascade root mounts (P1-CORE-01).
package main

var _ = registerRootMount(rootMount{Name: "approval", Order: 160, Mount: mountApprovalCmd})
