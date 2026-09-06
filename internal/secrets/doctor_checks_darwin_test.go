//go:build darwin

// Purpose: the darwin lane's assertion for check (a)'s platform note.
// SPORT: SECRETS_DOCTOR_CHECKS: ADD (keychain-reachable, darwin tests).

package secrets

import "testing"

// TestDoctorDarwinHasNoPlatformNote asserts macOS carries no tier-2 note,
// so the reachable check runs its real probe here.
func TestDoctorDarwinHasNoPlatformNote(t *testing.T) {
	if note := keychainPlatformNote(); note != "" {
		t.Fatalf("keychainPlatformNote() = %q on a tier-1 platform, want no note", note)
	}
}
