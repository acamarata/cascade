// Purpose: registers the `plugin` root mount.
// SPORT: cmd/cascade root mounts (P1-CORE-01).
package main

var _ = registerRootMount(rootMount{Name: "plugin", Order: 170, Mount: mountPluginCmd})
