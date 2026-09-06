//go:build linux

// Purpose: the linux lane's assertion for check (a)'s platform note.
// SPORT: SECRETS_DOCTOR_CHECKS: ADD (keychain-reachable, linux tests).

package secrets

import "testing"

// TestDoctorLinuxHasNoPlatformNote asserts Linux carries no tier-2 note:
// secret-service is a supported backend and the encrypted file vault is
// the documented fallback, so the check runs its real probe against
// whichever the broker selected.
func TestDoctorLinuxHasNoPlatformNote(t *testing.T) {
	if note := keychainPlatformNote(); note != "" {
		t.Fatalf("keychainPlatformNote() = %q on a tier-1 platform, want no note", note)
	}
}
