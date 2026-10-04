// Purpose: registers the `what` root mount (hidden alias for `recall what`).
// SPORT: cmd/cascade root mounts (P1-CORE-01).
package main

var _ = registerRootMount(rootMount{Name: "what", Order: 140, Mount: mountWhatCmd})
