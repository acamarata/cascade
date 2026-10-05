// Purpose: bind enrollment to the selected custody key.
// Inputs: custody, backend, clock. Outputs: bound identity or refusal.
// Constraints: explicit replacement only. SPORT: elevation trust binding.

package elevation

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"github.com/acamarata/cascade/pkg/cascade"
	"os"
	"path/filepath"
)

// ErrStaleFileTierEnrollment names the explicit replacement command.
func ErrStaleFileTierEnrollment() error {
	return cascade.New(cascade.KindConflict, "elevation: stale file-tier enrollment; run `cascade elevate-helper --enroll --replace-file-tier`")
}

// BoundTrust verifies that enrollment matches the selected custody key.
func BoundTrust(c Custody, b Backend, _ Clock) (string, ed25519.PublicKey, error) {
	ks, err := c.Signer()
	if err != nil {
		return "", nil, err
	}
	if b == nil {
		return "", nil, ErrHelperNotEnrolled()
	}
	rec, ok, err := b.Load()
	if err != nil {
		return "", nil, err
	}
	if !ok {
		return "", nil, ErrHelperNotEnrolled()
	}
	encoded, err := ks.PubKeyB64()
	if err != nil {
		return "", nil, err
	}
	pub, err := decodeCustodyPublicKey(encoded)
	if err != nil {
		return "", nil, err
	}
	recordPub, err := decodeCustodyPublicKey(rec.PubKeyB64)
	if err != nil {
		return "", nil, err
	}
	if !bytes.Equal(pub, recordPub) {
		stale, readErr := filePublicKey(c.dataDir)
		if readErr == nil && bytes.Equal(stale, recordPub) {
			return "", nil, ErrStaleFileTierEnrollment()
		}
		return "", nil, ErrAlreadyEnrolled()
	}
	fp, err := Fingerprint(encoded)
	return fp, pub, err
}
func decodeCustodyPublicKey(encoded string) (ed25519.PublicKey, error) {
	pub, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return nil, cascade.New(cascade.KindIntegrity, "elevation: invalid custody public key")
	}
	return pub, nil
}

// ReplaceFileTierEnrollment replaces only an enrollment bound to the stale file key.
func ReplaceFileTierEnrollment(c Custody, b Backend, clock Clock, dataDir string) (string, error) {
	ks, err := c.Signer()
	if err != nil {
		return "", err
	}
	rec, ok, err := b.Load()
	if err != nil {
		return "", err
	}
	old, err := filePublicKey(dataDir)
	if err != nil {
		return "", err
	}
	recordPub, err := decodeCustodyPublicKey(rec.PubKeyB64)
	if err != nil || !ok || !bytes.Equal(old, recordPub) {
		return "", ErrAlreadyEnrolled()
	}
	encoded, err := ks.PubKeyB64()
	if err != nil {
		return "", err
	}
	if _, err := decodeCustodyPublicKey(encoded); err != nil {
		return "", err
	}
	fp, err := Fingerprint(encoded)
	if err != nil {
		return "", err
	}
	next := TrustRecord{PubKeyB64: encoded, FingerprintSHA256: fp, EnrolledAt: clock.Now(), TOFUAcknowledged: true}
	if err := b.Save(next); err != nil {
		return "", err
	}
	if err := os.Remove(filepath.Join(dataDir, elevationKeyFileName)); err != nil {
		return "", cascade.Wrap(cascade.KindUnavailable, err, "elevation: remove stale file key after trust replacement")
	}
	return fp, nil
}
