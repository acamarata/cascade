//go:build darwin

// Purpose: check (a)'s platform note on macOS. The OS keychain is a
//
//	supported custody backend here, so there is no note and the check
//	performs its real probe.
//
// SPORT: SECRETS_DOCTOR_CHECKS: ADD (keychain-reachable, darwin).

package secrets

// keychainPlatformNote returns "" so the reachable check runs its probe.
func keychainPlatformNote() string { return "" }
