//go:build windows

// Purpose: check (a)'s platform note on Windows, a tier-2 platform. There
//
//	is no keychain backend here; the encrypted file vault is the
//	supported store, so the check reports that and passes. Reporting the
//	absence of a keychain as a failure would tell every operator on this
//	platform that a correctly configured host is broken.
//
// SPORT: SECRETS_DOCTOR_CHECKS: ADD (keychain-reachable, windows).

package secrets

// WindowsKeychainNote is the documented tier-2 refusal string check (a)
// emits on Windows. It is exported so the build-tagged sibling test can
// assert the exact text rather than a second copy of it.
const WindowsKeychainNote = "keychain: not available on Windows (tier-2); encrypted-file-vault in use"

// keychainPlatformNote returns the documented tier-2 note.
func keychainPlatformNote() string { return WindowsKeychainNote }
