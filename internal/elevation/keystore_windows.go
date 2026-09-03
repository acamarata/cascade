//go:build windows

// Purpose: the Windows tier-2 ElevationKeystore backend. Windows is
//
//	declared tier-2 for elevation by 06-FORGE-SPEC §2 and §D-24: every
//	elevated verb is refused rather than partially supported.
//
// Inputs: none.
// Outputs: ErrWindowsTier2 from every method.
// Constraints: compiles and links with CGO_ENABLED=0 (no CGO on this
//
//	platform at all — only darwin carries the CGO carve-out). Never
//	silently downgrades to an "allowed" result; every method call is a
//	typed refusal.
//
// SPORT: internal/elevation windowsKeystore/ADDED (P1-E04-W1-S07-T6).
package elevation

// windowsKeystore is the tier-2 refusal backend: it holds no state and
// every method returns ErrWindowsTier2.
type windowsKeystore struct{}

// NewKeystore returns the platform ElevationKeystore. On Windows this is
// always the tier-2 refusal backend.
func NewKeystore() ElevationKeystore {
	return windowsKeystore{}
}

// GenerateKey always refuses on Windows.
func (windowsKeystore) GenerateKey() error {
	return ErrWindowsTier2()
}

// PubKeyB64 always refuses on Windows.
func (windowsKeystore) PubKeyB64() (string, error) {
	return "", ErrWindowsTier2()
}

// Sign always refuses on Windows.
func (windowsKeystore) Sign(_ []byte) ([]byte, error) {
	return nil, ErrWindowsTier2()
}

// IsAvailable is always false on Windows: no elevation backend exists.
func (windowsKeystore) IsAvailable() bool {
	return false
}

// Tier always reports TierWindowsTier2.
func (windowsKeystore) Tier() StorageTier {
	return TierWindowsTier2
}
