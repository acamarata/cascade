// Purpose: registers the `plugin-namespaces` root mount.
// SPORT: cmd/cascade root mounts (P1-CORE-01).
package main

var _ = registerRootMount(rootMount{Name: "plugin-namespaces", Order: 270, Mount: mountPluginNamespaceCmds})
