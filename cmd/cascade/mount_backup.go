// Purpose: registers the `backup` root mount.
// SPORT: cmd/cascade root mounts (P1-CORE-01).
package main

var _ = registerRootMount(rootMount{Name: "backup", Order: 110, Mount: mountBackupCmd})
