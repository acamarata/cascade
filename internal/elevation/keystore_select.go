// Purpose: choosing between the platform keystore and the file fallback.
//
// One function, one rule: use the platform keystore when the host actually
//
//	has one, and the file keystore otherwise. The caller is told which, so
//	`cascade doctor` can report it and an operator is never unaware they
//	are on the weaker proof.
//
// SPORT: internal/elevation keystore-select/ADD — P1-W3-01 (W-3 gate).

package elevation

import "github.com/acamarata/cascade/pkg/cascade"

// SelectKeystore returns the strongest keystore this host can actually use.
//
// It NEVER silently downgrades: Tier() on the returned value names what was
// chosen, and TierFile is the disclosure. The preference order mirrors
// internal/secrets' SelectCustody exactly — platform backend first, file
// fallback second — because this is the same trade for the same reason, and
// two different fallback policies in one product would be a policy nobody
// could reason about.
//
// dir is the cascade data directory the file fallback would use. An empty
// dir means the fallback is not constructible, so a host with no platform
// keystore gets the platform one back and its own honest refusal, rather
// than a file keystore that cannot store anything.
func SelectKeystore(dir string) ElevationKeystore {
	// An existing file key wins over an "available" platform keystore. A
	// previous enrolment already resolved this host's answer, and switching
	// back would orphan the enrolled trust record — the operator would be
	// told the helper is enrolled while every attestation it signs verifies
	// against the wrong key.
	if dir != "" && fileKeyExists(dir) {
		return NewFileKeystore(dir)
	}
	platform := NewKeystore()
	if platform.IsAvailable() {
		return platform
	}
	if dir == "" {
		return platform
	}
	return NewFileKeystore(dir)
}

// Enroll generates this host's device key, falling back to the file
// keystore when the platform one is reachable but cannot actually store.
//
// The two are not the same question, and the W-3 gate proved it: on macOS
// the Keychain daemon answers (IsAvailable reports true) and the store then
// fails with OSStatus -34018 — errSecMissingEntitlement, because the binary
// is not signed. Availability is probed more coarsely than usability, so an
// enrolment that only consulted IsAvailable would refuse on a host where a
// perfectly good file key was possible.
//
// The returned tier is what the caller reports to the operator. A fallback
// that happened silently would be the dishonest version of this fix.
func Enroll(dir string) (ElevationKeystore, error) {
	selected := SelectKeystore(dir)
	err := selected.GenerateKey()
	if err == nil {
		return selected, nil
	}
	// Only a STORAGE failure falls back. An integrity error means a key
	// file exists and is damaged; silently writing a new one there would
	// orphan the enrolled record, which is the one outcome worse than
	// refusing.
	if dir == "" || selected.Tier() == TierFile || !storageFailure(err) {
		return nil, err
	}
	fallback := NewFileKeystore(dir)
	if ferr := fallback.GenerateKey(); ferr != nil {
		return nil, ferr
	}
	return fallback, nil
}

// storageFailure reports whether err is the platform keystore failing to
// STORE, as opposed to a damaged key or a refusal this layer must respect.
func storageFailure(err error) bool {
	return cascade.HasKind(err, cascade.KindUnavailable) ||
		cascade.HasKind(err, cascade.KindPermissionDenied)
}
