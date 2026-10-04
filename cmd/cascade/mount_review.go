// Purpose: registers the `review` root mount.
// SPORT: cmd/cascade root mounts (P1-CORE-01).
package main

var _ = registerRootMount(rootMount{Name: "review", Order: 250, Mount: mountReviewCmd})
