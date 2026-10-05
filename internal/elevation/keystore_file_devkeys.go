//go:build devkeys

// Purpose: development-only file signing. Inputs: data directory.
// Outputs: signing keystore. Constraints: exclusive atomic creation. SPORT: elevation devkeys.

package elevation

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// fileKeystore is the ElevationKeystore over one Ed25519 key file.
type fileKeystore struct {
	dir string
}

// NewFileKeystore builds a file-backed keystore rooted at dir.
func NewFileKeystore(dir string) ElevationKeystore { return fileKeystore{dir: dir} }

// path is the key file.
func (k fileKeystore) path() string { return filepath.Join(k.dir, elevationKeyFileName) }

// GenerateKey creates the key if it is absent. Idempotent: an existing key
// is left exactly as it is, never regenerated.
func (k fileKeystore) GenerateKey() error {
	if k.dir == "" {
		return ErrKeystoreUnavailable(errors.New("elevation: no data directory for the file keystore"))
	}
	if priv, err := k.load(); err == nil {
		zero(priv)
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "elevation: generating the device key")
	}
	defer zero(priv)
	if err := os.MkdirAll(k.dir, 0o700); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "elevation: creating the data directory")
	}
	encoded := []byte(base64.StdEncoding.EncodeToString(priv) + "\n")
	defer zero(encoded)
	if _, err := runtime.CreateFileAtomic(k.path(), encoded, 0o600); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "elevation: writing the device key")
	}
	return nil
}

// load reads the private key. A missing file returns fs.ErrNotExist
// unwrapped so GenerateKey can distinguish it from a real failure.
func (k fileKeystore) load() (ed25519.PrivateKey, error) {
	raw, err := os.ReadFile(k.path())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fs.ErrNotExist
	}
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "elevation: reading the device key")
	}
	defer zero(raw)
	decoded, derr := base64.StdEncoding.DecodeString(trimNewline(string(raw)))
	if derr != nil {
		return nil, cascade.Wrap(cascade.KindIntegrity, derr, "elevation: the device key file is unreadable")
	}
	if len(decoded) != ed25519.PrivateKeySize {
		zero(decoded)
		return nil, cascade.New(cascade.KindIntegrity, "elevation: the device key file is the wrong size")
	}
	return decoded, nil
}

// trimNewline drops one trailing newline without allocating a scanner.
func trimNewline(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}

// PubKeyB64 returns the enrolled public key.
func (k fileKeystore) PubKeyB64() (string, error) {
	priv, err := k.load()
	if errors.Is(err, fs.ErrNotExist) {
		return "", ErrHelperNotEnrolled()
	}
	if err != nil {
		return "", err
	}
	defer zero(priv)
	return base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey)), nil
}

// Sign signs payload with the device key.
//
// There is NO local-authentication step, because on a host with no keystore
// there is no authenticator to run one. This is exactly the weakness the
// tier name exists to disclose: the proof here is possession of a 0600 file
// in the operator's own data directory, not a human at the device.
func (k fileKeystore) Sign(payload []byte) ([]byte, error) {
	priv, err := k.load()
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrHelperNotEnrolled()
	}
	if err != nil {
		return nil, err
	}
	defer zero(priv)
	return ed25519.Sign(priv, payload), nil
}

// IsAvailable reports whether a key could be stored here at all.
func (k fileKeystore) IsAvailable() bool { return k.dir != "" }

// Tier always reports TierFile, before and after enrolment: an operator
// asking which backend they are on deserves the answer even when no key
// exists yet.
func (k fileKeystore) Tier() StorageTier { return TierFile }
