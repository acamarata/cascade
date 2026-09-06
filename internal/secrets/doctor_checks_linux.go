//go:build linux

// Purpose: check (a)'s platform note on Linux. secret-service is a
//
//	supported custody backend, with the encrypted file vault as the
//	documented fallback, so there is no note and the check performs its
//	real probe against whichever backend the broker selected.
//
// SPORT: SECRETS_DOCTOR_CHECKS: ADD (keychain-reachable, linux).

package secrets

// keychainPlatformNote returns "" so the reachable check runs its probe.
func keychainPlatformNote() string { return "" }
