//go:build linux

// Purpose: the linux passphrase presence keystore: an Ed25519 key in
//
//	<dataDir>/elevation/presence.key, sealed under a passphrase read from the
//	controlling terminal on every Sign and nowhere else. Each Sign derives,
//	opens, signs and zeroes; the keystore itself holds no secret.
//
// SPORT: internal/elevation presenceKeystore/ADDED (P1-SEC-25).

package elevation

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/crypto/scrypt"

	"github.com/acamarata/cascade/pkg/cascade"
)

const (
	presenceDirName       = "elevation"
	presenceFileName      = "presence.key"
	presenceAADPrefix     = "cascade-elevation-presence-v1\x00"
	presenceMinN          = 32768 // read floor: a file below this is refused
	presenceMinR          = 8
	presenceMaxMem        = 256 << 20 // accepted scrypt memory ceiling (128*n*r bytes)
	presenceMaxWork       = 1 << 22   // accepted n*r*p ceiling (4x the enrolled cost)
	presenceMinPassphrase = 12
	presenceSaltLen       = 16
	presenceMaxFile       = 16 << 10 // a valid key file is under 1 KiB
	tierPresence          = StorageTier("presence-passphrase")
)

// presenceEnrolN is the scrypt n written at enrolment (tests lower it, never below the floor);
// presenceWipeHook lets a test observe the secret buffers after Sign zeroed them (nil in production).
var (
	presenceEnrolN   = 131072
	presenceWipeHook func(pass, key, plain []byte)
)

// ttyPrompter reads one secret from a terminal and releases it (production: /dev/tty, echo off).
type ttyPrompter interface {
	ReadSecret(prompt string) ([]byte, error)
}

type presenceFile struct {
	V     int    `json:"v"`
	KDF   string `json:"kdf"`
	N     int    `json:"n"`
	R     int    `json:"r"`
	P     int    `json:"p"`
	Salt  string `json:"salt_b64"`
	Nonce string `json:"nonce_b64"`
	CT    string `json:"ct_b64"`
	Pub   string `json:"pub_b64"`
}

func (f presenceFile) marshal() []byte {
	raw, _ := json.Marshal(f) // a struct of strings and ints cannot fail to encode
	return append(raw, '\n')
}

type presenceSecrets struct{ pass, key, plain []byte }

func (s *presenceSecrets) zero() { zero(s.pass); zero(s.key); zero(s.plain) }

type presenceKeystore struct {
	dir    string
	tty    func() (ttyPrompter, error)
	getenv func(string) string
}

func newPresenceKeystore(dataDir string, tty func() (ttyPrompter, error), getenv func(string) string) ElevationKeystore {
	return &presenceKeystore{dir: dataDir, tty: tty, getenv: getenv}
}

func presencePath(dir string) string { return filepath.Join(dir, presenceDirName, presenceFileName) }

func presenceKeyExists(dir string) bool {
	_, err := os.Stat(presencePath(dir))
	return err == nil
}

// PresenceFingerprint reports the enrolled presence key's fingerprint
// without any prompt. found is false when no presence.key exists.
func PresenceFingerprint(dataDir string) (fingerprint string, found bool, err error) {
	f, err := readPresence(dataDir)
	if cascade.HasKind(err, cascade.KindNotFound) {
		return "", false, nil
	}
	if err == nil {
		fingerprint, err = Fingerprint(f.Pub)
	}
	return fingerprint, true, err
}

func presenceBad(why string) error {
	return cascade.New(cascade.KindIntegrity, "elevation: the presence key file is invalid: "+why)
}

func unreadable(err error) error {
	return cascade.Wrap(cascade.KindUnavailable, err, "elevation: reading the presence key")
}

// readPresence loads and validates presence.key before any prompt: missing is
// ErrHelperNotEnrolled; anything malformed, out of bounds, oversized or not a
// regular 0600 file owned by the current user is KindIntegrity.
func readPresence(dir string) (presenceFile, error) {
	f, _, err := readPresenceRaw(dir)
	return f, err
}

// readPresenceRaw is readPresence that also returns the bytes it parsed. It
// opens without following a symlink and checks the opened descriptor.
func readPresenceRaw(dir string) (presenceFile, []byte, error) {
	var f presenceFile
	fh, err := os.OpenFile(presencePath(dir), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0) // a FIFO must not block the open
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return f, nil, ErrHelperNotEnrolled()
	case errors.Is(err, syscall.ELOOP):
		return f, nil, presenceBad("it is a symlink")
	case err != nil:
		return f, nil, unreadable(err)
	}
	defer func() { _ = fh.Close() }()
	info, err := fh.Stat()
	if err != nil {
		return f, nil, unreadable(err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || !trustOwner(info) {
		return f, nil, presenceBad("it must be a regular file with mode 0600 owned by you")
	}
	raw, err := io.ReadAll(io.LimitReader(fh, presenceMaxFile+1))
	if err != nil {
		return f, nil, unreadable(err)
	}
	if len(raw) > presenceMaxFile {
		return f, nil, presenceBad("it is larger than any valid key file")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return f, nil, cascade.Wrap(cascade.KindIntegrity, err, "elevation: the presence key file is unreadable")
	}
	return f, raw, f.validate()
}

func (f presenceFile) validate() error {
	switch {
	case f.V != 1 || f.KDF != "scrypt":
		return presenceBad("unsupported version or kdf")
	case f.N < presenceMinN || f.N&(f.N-1) != 0 || f.R < presenceMinR || f.P < 1:
		return presenceBad("scrypt n, r or p is below the accepted floor, or n is not a power of two")
	case f.N > presenceMaxMem/128/f.R:
		return presenceBad("scrypt memory exceeds the accepted ceiling")
	case f.P > presenceMaxWork/f.N/f.R:
		return presenceBad("scrypt work exceeds the accepted ceiling")
	}
	for _, want := range []struct {
		name, b64 string
		n         int
	}{{"salt", f.Salt, presenceSaltLen}, {"nonce", f.Nonce, 12}, {"pub", f.Pub, ed25519.PublicKeySize}} {
		if raw, err := base64.StdEncoding.DecodeString(want.b64); err != nil || len(raw) != want.n {
			return presenceBad(want.name + " has the wrong size")
		}
	}
	if ct, err := base64.StdEncoding.DecodeString(f.CT); err != nil || len(ct) != ed25519.PrivateKeySize+16 {
		return presenceBad("ciphertext has the wrong size")
	}
	return nil
}

// readPassphrase opens the terminal, reads one secret and closes it. Every
// failure to obtain a secret is ErrAuthFailed and signs nothing.
func readPassphrase(tty func() (ttyPrompter, error), prompt string) ([]byte, error) {
	p, err := tty()
	var pass []byte
	if err == nil {
		pass, err = p.ReadSecret(prompt)
	}
	if err != nil {
		zero(pass)
		return nil, ErrAuthFailed(err)
	}
	return pass, nil
}

// tempName is a random sibling path for path, tagged with what it will hold.
func tempName(path, tag string) string {
	id, _ := cascade.NewID() // 128 random bits (on an entropy failure the name is fixed, and a clash makes CreateFileAtomic report created=false)
	return path + "." + tag + "." + string(id)
}

// derive stretches pass with the file's own scrypt parameters.
func (f presenceFile) derive(pass []byte) ([]byte, error) {
	salt, _ := base64.StdEncoding.DecodeString(f.Salt)
	key, err := scrypt.Key(pass, salt, f.N, f.R, f.P, 32)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindIntegrity, err, "elevation: the presence key parameters are unusable")
	}
	return key, nil
}

// presenceGCM is AES-256-GCM over the derived key.
func presenceGCM(key []byte) (gcm cipher.AEAD, err error) {
	block, err := aes.NewCipher(key)
	if err == nil {
		gcm, err = cipher.NewGCM(block)
	}
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInternal, err, "elevation: presence cipher")
	}
	return gcm, nil
}

func (f presenceFile) aad() []byte { return []byte(presenceAADPrefix + f.Pub) }

// open decrypts the private key into s. A wrong passphrase is ErrAuthFailed;
// a key whose seed does not give pub_b64 is KindIntegrity.
func (f presenceFile) open(s *presenceSecrets) error {
	key, err := f.derive(s.pass)
	if err != nil {
		return err
	}
	s.key = key
	gcm, err := presenceGCM(key)
	if err != nil {
		return err
	}
	nonce, _ := base64.StdEncoding.DecodeString(f.Nonce)
	ct, _ := base64.StdEncoding.DecodeString(f.CT)
	plain, err := gcm.Open(nil, nonce, ct, f.aad())
	s.plain = plain
	if err != nil {
		return ErrAuthFailed(nil)
	}
	pub, _ := base64.StdEncoding.DecodeString(f.Pub)
	if len(plain) != ed25519.PrivateKeySize {
		return cascade.New(cascade.KindIntegrity, "elevation: the presence key has the wrong size")
	}
	derived := ed25519.NewKeyFromSeed(plain[:ed25519.SeedSize]) // the stored public half is not trusted
	defer zero(derived)
	if !bytes.Equal(derived, plain) || !bytes.Equal(derived[ed25519.SeedSize:], pub) {
		return cascade.New(cascade.KindIntegrity, "elevation: the presence key does not match its recorded public key")
	}
	return nil
}

// GenerateKey refuses: a presence key exists only through EnrollPresence (prompt, audit, trust).
func (k *presenceKeystore) GenerateKey() error {
	return cascade.New(cascade.KindConflict, "elevation: the presence key is enrolled with `cascade elevate-helper --enroll --presence`")
}

// PubKeyB64 returns the recorded public key; it needs no passphrase.
func (k *presenceKeystore) PubKeyB64() (string, error) {
	f, err := readPresence(k.dir)
	return f.Pub, err
}

// Sign prompts on the controlling terminal, signs and zeroes; CASCADE_NO_INPUT=1 refuses before the terminal opens.
func (k *presenceKeystore) Sign(payload []byte) ([]byte, error) {
	if k.getenv("CASCADE_NO_INPUT") == "1" {
		return nil, ErrNoInput()
	}
	f, err := readPresence(k.dir)
	if err != nil {
		return nil, err
	}
	var s presenceSecrets
	defer func() {
		if presenceWipeHook != nil {
			presenceWipeHook(s.pass, s.key, s.plain)
		}
	}()
	defer s.zero()
	if s.pass, err = readPassphrase(k.tty, "Cascade presence passphrase: "); err != nil {
		return nil, err
	}
	if err := f.open(&s); err != nil {
		return nil, err
	}
	return ed25519.Sign(s.plain, payload), nil
}

func (k *presenceKeystore) IsAvailable() bool { return presenceKeyExists(k.dir) }

func (k *presenceKeystore) Tier() StorageTier { return tierPresence }
