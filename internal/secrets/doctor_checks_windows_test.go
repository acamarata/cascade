//go:build windows

// Purpose: the windows lane's assertion for check (a). Windows is tier-2:
//
//	there is no keychain backend, the encrypted file vault is the
//	supported store, and the check says so and passes.
//
// SPORT: SECRETS_DOCTOR_CHECKS: ADD (keychain-reachable, windows tests).

package secrets

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/doctor"
)

// TestDoctorWindowsRefusal asserts the exact documented refusal string and
// the passing result. The string is compared against the literal the
// contract fixes, not against the constant the implementation reads, so
// this is not a table asserted against a second copy of itself.
func TestDoctorWindowsRefusal(t *testing.T) {
	const want = "keychain: not available on Windows (tier-2); encrypted-file-vault in use"
	if WindowsKeychainNote != want {
		t.Fatalf("WindowsKeychainNote = %q, want the documented refusal %q", WindowsKeychainNote, want)
	}
	check := keychainReachableCheck{}
	res, err := check.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != doctor.StatusOK {
		t.Fatalf("Run = %+v, want ok: the note is informational on a tier-2 platform", res)
	}
	if res.Message != want {
		t.Fatalf("Run message = %q, want %q", res.Message, want)
	}
}
