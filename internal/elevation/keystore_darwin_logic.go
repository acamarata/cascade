//go:build darwin && cgo

// Purpose: darwinKeystore, the ElevationKeystore implementation built on
//
//	darwinBridge (keystore_darwin.go). Split out of that file purely to
//	respect the 300-line file cap (Art.10.3) — this file and
//	keystore_darwin.go together are one logical unit: the CGO/Objective-C
//	bridge there, the platform-agnostic Go control flow here.
//
// SPORT: internal/elevation darwinKeystore/ADDED (P1-E04-W1-S07-T6).

package elevation

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"

	"github.com/acamarata/cascade/pkg/cascade"
)

// darwinKeystore is the darwin ElevationKeystore. It holds no in-memory
// key material between calls; every method re-reads the Keychain via
// bridge.
type darwinKeystore struct{ bridge darwinBridge }

// edKeyRandSource is the entropy source GenerateKey passes to
// ed25519.GenerateKey. It is crypto/rand.Reader in production; tests
// override the package var to a reader that returns an error, the only
// way to reach GenerateKey's rand-failure branch without an actual
// entropy-source failure (Art.1: overridden only from _test.go).
var edKeyRandSource = rand.Reader

// NewKeystore returns the platform ElevationKeystore.
func NewKeystore() ElevationKeystore { return darwinKeystore{bridge: cgoBridge{}} }

func (k darwinKeystore) IsAvailable() bool { return k.bridge.laProbe() }

func (k darwinKeystore) Tier() StorageTier {
	if _, err := k.loadPublic(); err == nil {
		return TierOSKeychain
	}
	return TierUnavailable
}

// GenerateKey is idempotent: an existing enrolled key is left untouched.
func (k darwinKeystore) GenerateKey() error {
	if _, err := k.loadPublic(); err == nil {
		return nil
	}
	pub, priv, err := ed25519.GenerateKey(edKeyRandSource)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "elevation: generate ed25519 key")
	}
	defer zero(priv)
	if status := k.bridge.keychainStore(darwinPrivAcct, priv, true); status != 0 {
		return cascade.Newf(cascade.KindUnavailable, "elevation: store private key in Keychain (OSStatus %d)", status)
	}
	if status := k.bridge.keychainStore(darwinPubAcct, pub, false); status != 0 {
		return cascade.Newf(cascade.KindUnavailable, "elevation: store public key in Keychain (OSStatus %d)", status)
	}
	return nil
}

func (k darwinKeystore) PubKeyB64() (string, error) {
	pub, err := k.loadPublic()
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(pub), nil
}

// Sign authenticates and signs atomically: bridge.signingKeyLoad is one
// call that (in production) evaluates LocalAuthentication and reads the
// private key under that same authenticated context, so no auth token is
// ever held in Go between calls.
func (k darwinKeystore) Sign(payload []byte) ([]byte, error) {
	buf, n := k.bridge.signingKeyLoad(darwinAuthReason, ed25519.PrivateKeySize)
	switch {
	case n == -1:
		return nil, ErrKeystoreUnavailable(nil)
	case n == -2:
		return nil, ErrAuthFailed(nil)
	case n == -3:
		zero(buf)
		return nil, ErrHelperNotEnrolled()
	case n < 0 || n != len(buf):
		zero(buf)
		return nil, cascade.Newf(cascade.KindIntegrity, "elevation: unexpected private key length from Keychain (%d)", n)
	}
	priv := ed25519.PrivateKey(buf)
	sig := ed25519.Sign(priv, payload)
	zero(buf)
	return sig, nil
}

func (k darwinKeystore) loadPublic() ([]byte, error) {
	pub, ok := k.bridge.keychainLoadPlain(darwinPubAcct, ed25519.PublicKeySize)
	if !ok {
		return nil, ErrHelperNotEnrolled()
	}
	return pub, nil
}

// zero overwrites b's bytes so key material does not linger in memory
// beyond the call that needed it.
func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
