// Purpose: registers the `docs` root mount.
// SPORT: cmd/cascade root mounts (P1-CORE-01).
package main

var _ = registerRootMount(rootMount{Name: "docs", Order: 268, Mount: mountDocsCmd})
