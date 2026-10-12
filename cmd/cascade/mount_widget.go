// Purpose: registers the `widget` root mount.
// SPORT: cmd/cascade root mounts (P1-WID-04).
package main

var _ = registerRootMount(rootMount{Name: "widget", Order: 265, Mount: mountWidgetCmd})
