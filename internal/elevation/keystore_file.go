// Purpose: the file-backed elevation keystore — the fallback used on a
//
//	host with no hardware or OS keystore, so elevated verbs are possible on
//	a Linux server, in a container, and from a CGO-free release binary.
//
// WHY IT EXISTS. The Wave-3 hardening gate proved that NO elevated verb
//
//	could succeed from any shipped artifact: release binaries are built
//	CGO_ENABLED=0, so the platform backends are not compiled in at all, and
//	enrolment failed with "no hardware/OS keystore is available on this
//	host". `vault get`, `vault rotate` and `vault grant` were therefore all
//	unusable in the product — an Art.6 known-broken surface, and after
//	R-14.243 the thing `cascade run` depends on.
//
// WHAT IT IS, AND IS NOT. This is a WEAKER proof and says so everywhere it
//
//	can. A hardware keystore proves a human authenticated at the device;
//	this proves possession of a 0600 file in the operator's own data
//	directory. It is the same trade internal/secrets already makes when
//	SelectCustody falls back from the OS keychain to the encrypted file
//	vault, and it is acceptable for the same reason and only under the same
//	conditions: it is selected ONLY when no platform keystore is available,
//	it reports its own tier so `cascade doctor` can name it, and it never
//	claims to be the stronger one.
//
// Inputs: a directory (the cascade data dir).
// Outputs: an ElevationKeystore over one Ed25519 key file.
// Constraints: fails closed. A key file that cannot be created, read, or
//
//	parsed is an error, never a silently regenerated key — regenerating
//	would orphan the enrolled trust record and read to the operator as a
//	working enrolment.
//
// SPORT: internal/elevation file-keystore/ADD — P1-W3-01 (W-3 gate, Art.9).

package elevation

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TierFile means the key is stored in a 0600 file in the cascade data
// directory, because no hardware or OS keystore is available on this host.
// It is the weakest tier this repo offers and is always reported, never
// silently substituted.
const TierFile StorageTier = "file"

// elevationKeyFileName is the key file's name inside the data directory.
const elevationKeyFileName = "elevation.key"

// fileKeyExists reports whether a device key file is already present in
// dir. Used by SelectKeystore to keep a host on the backend a previous
// enrolment resolved it to.
func fileKeyExists(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, elevationKeyFileName))
	return err == nil
}

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
	if _, err := k.load(); err == nil {
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
	if err := os.WriteFile(k.path(), encoded, 0o600); err != nil {
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
