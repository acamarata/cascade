//go:build linux

// Purpose: the presence tier's /dev/tty prompter and its enrolment ceremony (fresh
//
//	or rotation; one at a time under a flock, one key snapshot per run).
//
// SPORT: internal/elevation presenceEnroll/ADDED (P1-SEC-25).

package elevation

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

const presenceMaxInput = 1024

func init() {
	presenceSource = &CustodySource{Tier: CustodyPresence, Name: "presence-passphrase", Open: func(dir string) (ElevationKeystore, bool) {
		return newPresenceKeystore(dir, openControllingTTY, os.Getenv), dir != "" && presenceKeyExists(dir)
	}}
	presenceEnroll = func(dir string, trust Backend, clock Clock, aud PresenceAuditor) (string, error) {
		return enrollPresence(dir, openControllingTTY, os.Getenv, trust, clock, aud)
	}
}

type devTTY struct{ f *os.File }

// openControllingTTY opens /dev/tty; with no controlling terminal it fails, never falls back.
func openControllingTTY() (ttyPrompter, error) {
	f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("no controlling terminal: %w", err)
	}
	return &devTTY{f: f}, nil
}

// ReadSecret prints prompt, reads one line with echo off into a fixed buffer (no append copies) and restores the terminal.
func (d *devTTY) ReadSecret(prompt string) ([]byte, error) {
	defer func() { _ = d.f.Close() }() // single use
	fd := int(d.f.Fd())
	saved, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err == nil {
		quiet := *saved
		quiet.Lflag &^= unix.ECHO
		err = unix.IoctlSetTermios(fd, unix.TCSETS, &quiet)
	}
	if err != nil {
		return nil, fmt.Errorf("cannot turn terminal echo off (not a terminal?): %w", err)
	}
	defer func() {
		_ = unix.IoctlSetTermios(fd, unix.TCSETS, saved)
		_, _ = d.f.WriteString("\n")
	}()
	_, _ = d.f.WriteString(prompt)
	buf, one := make([]byte, 0, presenceMaxInput), make([]byte, 1)
	defer zero(one)
	for {
		n, err := d.f.Read(one)
		if n == 1 {
			if one[0] == '\n' || one[0] == '\r' {
				return buf, nil
			}
			if len(buf) == presenceMaxInput {
				zero(buf)
				return nil, errors.New("passphrase is too long")
			}
			buf = append(buf, one[0])
		}
		if err != nil {
			if len(buf) > 0 { // end of input after text
				return buf, nil
			}
			return nil, err
		}
	}
}

// enrollPresence runs the ceremony and returns the new fingerprint.
func enrollPresence(dir string, tty func() (ttyPrompter, error), getenv func(string) string,
	trust Backend, clock Clock, aud PresenceAuditor) (string, error) {
	if getenv("CASCADE_NO_INPUT") == "1" {
		return "", ErrNoInput()
	}
	if dir == "" || aud == nil || trust == nil {
		return "", ErrKeystoreUnavailable(errors.New("elevation: presence enrolment needs a data directory, a trust store and an audit log"))
	}
	unlock, err := lockEnrolment(dir)
	if err != nil {
		return "", err
	}
	defer unlock()
	old, rotation, err := checkPresenceEnrolment(dir, trust)
	if err != nil {
		return "", err
	}
	if err := proveOldPassphrase(old, rotation, tty); err != nil {
		return "", err
	}
	file, err := newPresenceFile(tty)
	if err != nil {
		return "", err
	}
	fp, _ := Fingerprint(file.Pub) // file.Pub is the key just generated
	finish, err := aud.Begin(fp, map[bool]string{false: "fresh", true: "rotation"}[rotation])
	if err != nil {
		return "", err
	}
	err = commitPresence(dir, file, old, rotation, trust, clock)
	if got, _, perr := PresenceFingerprint(dir); err == nil && (perr != nil || got != fp) {
		err = cascade.New(cascade.KindIntegrity, "elevation: presence.key changed while it was being enrolled")
	}
	finish(err)
	return fp, err
}

// lockEnrolment takes the enrolment lock; closing releases it.
func lockEnrolment(dir string) (func(), error) {
	d := filepath.Join(dir, presenceDirName)
	err := os.MkdirAll(d, 0o700)
	var f *os.File
	if err == nil {
		f, err = os.OpenFile(filepath.Join(d, ".enroll.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	}
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "elevation: opening the enrolment lock")
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, cascade.New(cascade.KindConflict, "elevation: another presence enrolment is running")
	}
	return func() { _ = f.Close() }, nil
}

// checkPresenceEnrolment takes the run's one snapshot of the key, decides fresh or rotation, and refuses another key's trust record.
func checkPresenceEnrolment(dir string, trust Backend) (old presenceFile, rotation bool, err error) {
	switch old, err = readPresence(dir); {
	case err == nil:
		rotation = true
	case !cascade.HasKind(err, cascade.KindNotFound):
		return old, false, err
	}
	rec, ok, lerr := trust.Load()
	if lerr != nil {
		return old, false, cascade.Wrap(cascade.KindUnavailable, lerr, "elevation: read trust record")
	}
	if ok && (!rotation || rec.PubKeyB64 != old.Pub) {
		return old, false, ErrAlreadyEnrolled()
	}
	return old, rotation, nil
}

// proveOldPassphrase decrypts the snapshot of a rotation; a wrong passphrase is ErrAuthFailed.
func proveOldPassphrase(old presenceFile, rotation bool, tty func() (ttyPrompter, error)) error {
	if !rotation {
		return nil
	}
	pass, err := readPassphrase(tty, "Current presence passphrase: ")
	s := presenceSecrets{pass: pass}
	defer s.zero()
	if err != nil {
		return err
	}
	return old.open(&s)
}

// newPresenceFile prompts for the new passphrase twice and seals a new key.
func newPresenceFile(tty func() (ttyPrompter, error)) (presenceFile, error) {
	pass, err := readPassphrase(tty, "New presence passphrase (at least 12 bytes): ")
	s := presenceSecrets{pass: pass}
	defer s.zero()
	if err != nil {
		return presenceFile{}, err
	}
	again, err := readPassphrase(tty, "Repeat the passphrase: ")
	defer zero(again)
	switch {
	case err != nil:
		return presenceFile{}, err
	case !bytes.Equal(pass, again):
		return presenceFile{}, cascade.New(cascade.KindInvalidInput, "elevation: the two passphrases differ")
	case len(pass) < presenceMinPassphrase:
		return presenceFile{}, cascade.Newf(cascade.KindInvalidInput, "elevation: the passphrase needs at least %d bytes", presenceMinPassphrase)
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return presenceFile{}, cascade.Wrap(cascade.KindInternal, err, "elevation: generating the presence key")
	}
	s.plain = priv
	return sealPresence(pass, priv)
}

// sealPresence encrypts priv under pass into a new presenceFile.
func sealPresence(pass []byte, priv ed25519.PrivateKey) (presenceFile, error) {
	f := presenceFile{V: 1, KDF: "scrypt", N: max(presenceEnrolN, presenceMinN), R: presenceMinR, P: 1,
		Pub: base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey))}
	random := make([]byte, presenceSaltLen+12)
	if _, err := io.ReadFull(rand.Reader, random); err != nil {
		return f, cascade.Wrap(cascade.KindInternal, err, "elevation: presence salt and nonce")
	}
	salt, nonce := random[:presenceSaltLen], random[presenceSaltLen:]
	f.Salt, f.Nonce = base64.StdEncoding.EncodeToString(salt), base64.StdEncoding.EncodeToString(nonce)
	key, err := f.derive(pass)
	if err != nil {
		return f, err
	}
	defer zero(key)
	gcm, err := presenceGCM(key)
	if err != nil {
		return f, err
	}
	f.CT = base64.StdEncoding.EncodeToString(gcm.Seal(nil, nonce, priv, f.aad()))
	return f, nil
}

// commitPresence publishes the file, then the trust record; a rotation first checks
// presence.key is still the snapshot the old passphrase opened. If the trust write
// fails and the record is provably not the new one, the previous key (or none) is
// restored; if that is unprovable nothing is rolled back.
func commitPresence(dir string, file, old presenceFile, rotation bool, trust Backend, clock Clock) error {
	path := presencePath(dir)
	var prev []byte
	if rotation {
		cur, raw, rerr := readPresenceRaw(dir)
		if rerr != nil || cur != old {
			return cascade.New(cascade.KindConflict, "elevation: presence.key changed during the enrolment; nothing was written")
		}
		prev = raw
	}
	if err := writePresenceFile(path, file.marshal(), rotation); err != nil {
		return err
	}
	fp, _ := Fingerprint(file.Pub)
	err := trust.Save(TrustRecord{PubKeyB64: file.Pub, FingerprintSHA256: fp, EnrolledAt: clock.Now(), TOFUAcknowledged: true})
	if err == nil {
		return nil
	}
	cur, ok, lerr := trust.Load()
	switch {
	case lerr == nil && ok && cur.PubKeyB64 == file.Pub:
		return nil // published; only the durability step failed: key and trust agree
	case lerr != nil: // whether the record was published is unknown: roll nothing back
		msg := "elevation: the trust record could not be written or read back; presence.key was replaced"
		if keep := tempName(path, "previous"); !rotation {
			return cascade.Wrap(cascade.KindIntegrity, errors.Join(err, lerr), msg)
		} else if created, kerr := runtime.CreateFileAtomic(keep, prev, 0o600); kerr != nil || !created {
			err = errors.Join(err, kerr, errors.New("elevation: the old presence key could not be kept"))
		} else {
			msg += "; the old key is kept at " + keep
		}
		return cascade.Wrap(cascade.KindIntegrity, errors.Join(err, lerr), msg)
	}
	var restore error
	if rotation { // the new key stays in place until the old one is back
		restore = runtime.WriteFileAtomic(path, prev, 0o600)
	} else {
		restore = os.Remove(path)
	}
	if restore != nil {
		return cascade.Wrap(cascade.KindIntegrity, errors.Join(err, restore), "elevation: trust write failed and the previous presence key could not be restored")
	}
	return cascade.Wrap(cascade.KindUnavailable, err, "elevation: write trust record")
}

// writePresenceFile publishes data: create-once at path, or via a random temp name for a rotation.
func writePresenceFile(path string, data []byte, rotation bool) error {
	target := path
	if rotation {
		target = tempName(path, "new")
	}
	created, err := runtime.CreateFileAtomic(target, data, 0o600)
	if err != nil {
		if created { // linked, then a later step failed: leave nothing behind
			_ = os.Remove(target)
		}
		return cascade.Wrap(cascade.KindUnavailable, err, "elevation: writing the presence key")
	}
	if !created {
		return ErrAlreadyEnrolled()
	}
	if rotation {
		if err := os.Rename(target, path); err != nil {
			_ = os.Remove(target)
			return cascade.Wrap(cascade.KindUnavailable, err, "elevation: replacing the presence key")
		}
	}
	return nil
}
