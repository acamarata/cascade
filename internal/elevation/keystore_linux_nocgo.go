//go:build linux && !cgo

// Purpose: the CGO_ENABLED=0 fallback for linux, mirroring
//
//	keystore_darwin_nocgo.go's rationale exactly: keystore_linux.go
//	(linux && cgo) disappears from the build when CGO is disabled, and
//	this file supplies the same NewKeystore() symbol so `CGO_ENABLED=0
//	go build ./...` still succeeds, falling back to a keystore that
//	fails closed on every operation rather than a missing symbol.
//
// SPORT: internal/elevation linuxNoCGOKeystore/ADDED (P1-E04-W1-S07-T6).
package elevation

// linuxNoCGOKeystore is the fallback used when the binary was built with
// CGO_ENABLED=0: no PAM/keyring access is possible, so every operation
// fails closed.
type linuxNoCGOKeystore struct{}

// NewKeystore returns the platform ElevationKeystore.
func NewKeystore() ElevationKeystore { return linuxNoCGOKeystore{} }

func (linuxNoCGOKeystore) IsAvailable() bool { return false }

func (linuxNoCGOKeystore) Tier() StorageTier { return TierUnavailable }

func (linuxNoCGOKeystore) GenerateKey() error {
	return ErrKeystoreUnavailable(nil)
}

func (linuxNoCGOKeystore) PubKeyB64() (string, error) {
	return "", ErrKeystoreUnavailable(nil)
}

func (linuxNoCGOKeystore) Sign(_ []byte) ([]byte, error) {
	return nil, ErrKeystoreUnavailable(nil)
}
