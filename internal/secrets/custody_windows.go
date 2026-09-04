//go:build windows

// Purpose: the Windows custody backend - there is none. Windows is a
//
//	tier-2 platform for the vault: storage works through the encrypted
//	file vault, and the elevated verbs (get, rotate) refuse.
//
// Inputs: a Config (unused here beyond satisfying the shared signature).
// Outputs: no platform Custody, plus the tier-2 elevated-verb refusal the
//
//	broker consults.
//
// Constraints: refusing is a typed error, never a panic and never a silent
//
//	success. Returning no platform backend is what makes SelectCustody
//	fall through to the encrypted file vault, which is the documented
//	tier-2 behaviour rather than an accident.
//
// SPORT: internal/secrets Custody/ADDED (windows tier-2).

package secrets

import "github.com/acamarata/cascade/pkg/cascade"

// errWindowsTier2Vault is the tier-2 refusal. KindUnsupported: the verb is
// recognised but this platform implements no elevation root of trust to
// authorise it with, so it cannot be performed here at all.
//
// It is unexported and defined in this build-tagged file on purpose: an
// exported error constructor no non-Windows build could ever reach would be
// undead code on every other platform.
func errWindowsTier2Vault() error {
	return cascade.New(cascade.KindUnsupported,
		"secrets: elevated vault verbs (get, rotate) are refused on Windows; it is a tier-2 platform with no local elevation helper")
}

// platformCustody reports that Windows has no native custody backend, so
// SelectCustody falls through to the encrypted file vault.
func platformCustody(_ Config) (Custody, error) {
	return nil, errWindowsTier2Vault()
}

// platformElevatedRefusal returns the tier-2 refusal for elevated vault
// verbs on Windows. Non-elevated storage (set, list, import, delete)
// continues to work against the file vault.
func platformElevatedRefusal() error { return errWindowsTier2Vault() }
