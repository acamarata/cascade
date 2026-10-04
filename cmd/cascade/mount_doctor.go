// Purpose: registers the `doctor` root mount.
// SPORT: cmd/cascade root mounts (P1-CORE-01).
package main

var _ = registerRootMount(rootMount{Name: "doctor", Order: 80, Mount: mountDoctorCmd})
