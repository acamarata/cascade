//go:build darwin && !cgo

// Purpose: the CGO_ENABLED=0 fallback for darwin. Requirement of this
//
//	ticket's contract: "CGO_ENABLED=0 go build ./... MUST still succeed,
//	falling back to the non-hardware keystore. Core must never require
//	CGO." Any file with `import "C"` disappears entirely from the build
//	when CGO is disabled (verified directly: the go toolchain excludes
//	cgo-importing files as if their build constraints did not match), so
//	keystore_darwin.go (darwin && cgo) is invisible here and this file
//	supplies the same NewKeystore() symbol instead.
//
// Inputs: none.
// Outputs: a keystore that reports itself unavailable and fails closed on
//
//	every operation that would otherwise touch the Keychain.
//
// Constraints: never returns a success value — GenerateKey/PubKeyB64/Sign
//
//	all report ErrKeystoreUnavailable rather than pretending a key exists.
//
// SPORT: internal/elevation darwinNoCGOKeystore/ADDED (P1-E04-W1-S07-T6).
package elevation

// darwinNoCGOKeystore is the fallback used when the binary was built with
// CGO_ENABLED=0: no Keychain/LocalAuthentication access is possible, so
// every operation fails closed rather than silently succeeding.
type darwinNoCGOKeystore struct{}

// NewKeystore returns the platform ElevationKeystore.
func NewKeystore() ElevationKeystore { return darwinNoCGOKeystore{} }

func (darwinNoCGOKeystore) IsAvailable() bool { return false }

func (darwinNoCGOKeystore) Tier() StorageTier { return TierUnavailable }

func (darwinNoCGOKeystore) GenerateKey() error {
	return ErrKeystoreUnavailable(nil)
}

func (darwinNoCGOKeystore) PubKeyB64() (string, error) {
	return "", ErrKeystoreUnavailable(nil)
}

func (darwinNoCGOKeystore) Sign(_ []byte) ([]byte, error) {
	return nil, ErrKeystoreUnavailable(nil)
}
