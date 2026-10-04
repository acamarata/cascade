// Purpose: registers the `elevate-helper` root mount.
// SPORT: cmd/cascade root mounts (P1-CORE-01).
package main

var _ = registerRootMount(rootMount{Name: "elevate-helper", Order: 90, Mount: mountElevateHelperCmd})
