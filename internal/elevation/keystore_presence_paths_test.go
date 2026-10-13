//go:build linux

// Purpose: presence tests for the paths the acceptance tests do not reach:
//
//	the real prompter on a pty, trust-store failures and their rollback,
//	enrolment refusals, and the small keystore methods.
//
// SPORT: internal/elevation presence tests/ADDED (P1-SEC-25).
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
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/acamarata/cascade/pkg/cascade"
)

type saveFails struct{ Backend }

func (saveFails) Save(TrustRecord) error { return errors.New("disk full") }

func OpenPtyForTest(t *testing.T) (master, slave *os.File) {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("no /dev/ptmx: %v", err)
	}
	// Control, not Fd(): Fd() would put the master in blocking mode and disable read deadlines.
	var n int
	raw, _ := master.SyscallConn()
	err = raw.Control(func(fd uintptr) {
		var err error
		if n, err = unix.IoctlGetInt(int(fd), unix.TIOCGPTN); err == nil {
			err = unix.IoctlSetPointerInt(int(fd), unix.TIOCSPTLCK, 0)
		}
		if err != nil {
			t.Errorf("pty ioctl: %v", err)
		}
	})
	if err == nil {
		slave, err = os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|syscall.O_NOCTTY, 0)
	}
	if err != nil {
		t.Fatalf("opening the pty: %v", err)
	}
	return master, slave
}

func TestKeystoreReadSecretOnAPty(t *testing.T) {
	master, slave := OpenPtyForTest(t)
	defer func() { _ = master.Close() }()
	defer func() { _ = slave.Close() }()
	go func() {
		var seen []byte
		for buf := make([]byte, 64); !bytes.Contains(seen, []byte("phrase: ")); {
			n, err := master.Read(buf)
			if seen = append(seen, buf[:n]...); err != nil {
				return
			}
		}
		_, _ = io.WriteString(master, presTestPass+"\n")
	}()
	fd, err := unix.Dup(int(slave.Fd())) // ReadSecret closes slave; fd still names the terminal
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Close(fd) }()
	got, err := (&devTTY{f: slave}).ReadSecret("Type the phrase: ")
	if err != nil || string(got) != presTestPass {
		t.Fatalf("ReadSecret = %q, %v; want the typed passphrase", got, err)
	}
	_ = master.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	echoed, _ := io.ReadAll(master)
	if strings.Contains(string(echoed), presTestPass) {
		t.Fatalf("the typed passphrase was echoed: %q", echoed)
	}
	if st, err := unix.IoctlGetTermios(fd, unix.TCGETS); err != nil || st.Lflag&unix.ECHO == 0 {
		t.Fatalf("echo was not restored after the read: %v", err)
	}
	t.Run("a pipe is not a terminal", func(t *testing.T) {
		r, w, _ := os.Pipe()
		defer func() { _ = r.Close() }()
		defer func() { _ = w.Close() }()
		if got, err := (&devTTY{f: r}).ReadSecret("p: "); err == nil || got != nil {
			t.Fatalf("a non-terminal handle returned %q, %v; want a refusal", got, err)
		}
	})
}

func TestKeystorePresenceSmallPaths(t *testing.T) {
	dir := t.TempDir()
	ks := newPresenceKeystore(dir, newPresTTY().open, noEnv)
	if err := ks.GenerateKey(); !cascade.HasKind(err, cascade.KindConflict) || ks.IsAvailable() {
		t.Fatalf("GenerateKey=%v available=%v; want it to refuse (enrol is the ceremony) and no key", err, ks.IsAvailable())
	}
	if _, err := ks.PubKeyB64(); !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("PubKeyB64 before enrolment = %v, want KindNotFound", err)
	}
	if fp, found, err := PresenceFingerprint(dir); fp != "" || found || err != nil {
		t.Fatalf("PresenceFingerprint with no key = %q,%v,%v", fp, found, err)
	}
	enrolled, fp := presEnrol(t, presTestPass)
	ks = newPresenceKeystore(enrolled, newPresTTY().open, noEnv)
	if got, found, err := PresenceFingerprint(enrolled); err != nil || !found || got != fp || !ks.IsAvailable() || ks.Tier() != tierPresence {
		t.Fatalf("enrolled: fp=%q found=%v err=%v", got, found, err)
	}
	if err := ks.GenerateKey(); !cascade.HasKind(err, cascade.KindConflict) {
		t.Fatalf("GenerateKey over an enrolled key = %v; a bare enrol must not bind it without an audit record", err)
	}
	if err := os.WriteFile(presencePath(enrolled), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, found, err := PresenceFingerprint(enrolled); !found || !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("corrupt key: found=%v err=%v; want found and KindIntegrity", found, err)
	}
	for name, tty := range map[string]func() (ttyPrompter, error){
		"no terminal":  func() (ttyPrompter, error) { return nil, errors.New("no tty") },
		"read failure": newPresTTY().open, // a prompter with no answers fails to read
	} {
		if pass, err := readPassphrase(tty, "p: "); pass != nil || err == nil || !strings.HasPrefix(err.Error(), ErrAuthFailed(nil).Error()) {
			t.Fatalf("%s: readPassphrase = %q, %v; want ErrAuthFailed", name, pass, err)
		}
	}
}

// presRefused runs an enrolment that must refuse with kind before any audit record.
func presRefused(t *testing.T, dir string, kind cascade.Kind, answers ...string) (*presTTY, *presAudit) {
	t.Helper()
	tty, aud := newPresTTY(answers...), &presAudit{}
	_, err := enrollPresence(dir, tty.open, noEnv, NewFileBackend(dir), presClock(), aud)
	if !cascade.HasKind(err, kind) || len(aud.Begun) != 0 {
		t.Fatalf("err=%v audit=%d; want %v and no audit record", err, len(aud.Begun), kind)
	}
	return tty, aud
}

func TestEnrollPresenceRefusals(t *testing.T) {
	other := TrustRecord{PubKeyB64: "b3RoZXI=", FingerprintSHA256: "x", TOFUAcknowledged: true}
	t.Run("production entry refuses without input or an audit log", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("CASCADE_NO_INPUT", "1")
		if _, err := EnrollPresence(dir, NewFileBackend(dir), presClock(), &presAudit{}); err == nil || err.Error() != ErrNoInput().Error() {
			t.Fatalf("EnrollPresence under NO_INPUT = %v", err)
		}
		t.Setenv("CASCADE_NO_INPUT", "")
		if _, err := EnrollPresence(dir, NewFileBackend(dir), presClock(), nil); !cascade.HasKind(err, cascade.KindUnavailable) {
			t.Fatalf("EnrollPresence without an auditor = %v, want KindUnavailable", err)
		}
	})
	t.Run("a fresh enrolment over a trust record for another key", func(t *testing.T) {
		dir := t.TempDir()
		if err := NewFileBackend(dir).Save(other); err != nil {
			t.Fatal(err)
		}
		if tty, _ := presRefused(t, dir, cascade.KindConflict, presTestPass, presTestPass); tty.calls != 0 || presenceKeyExists(dir) {
			t.Fatalf("prompts=%d key=%v; want nothing done", tty.calls, presenceKeyExists(dir))
		}
	})
	t.Run("a rotation over a trust record for another key", func(t *testing.T) {
		dir, _ := presEnrol(t, presTestPass)
		before := readFile(t, presencePath(dir))
		if err := NewFileBackend(dir).Save(other); err != nil {
			t.Fatal(err)
		}
		if tty, _ := presRefused(t, dir, cascade.KindConflict, presTestPass, presTestNewOne, presTestNewOne); tty.calls != 0 || !bytes.Equal(before, readFile(t, presencePath(dir))) {
			t.Fatalf("prompts=%d; want no prompt and an unchanged key", tty.calls)
		}
	})
}

func TestEnrollPresenceRefusesUnsafeState(t *testing.T) {
	t.Run("a corrupt key file is never overwritten", func(t *testing.T) {
		dir, _ := presEnrol(t, presTestPass)
		if err := os.WriteFile(presencePath(dir), []byte("garbage"), 0o600); err != nil {
			t.Fatal(err)
		}
		presRefused(t, dir, cascade.KindIntegrity, presTestPass, presTestNewOne, presTestNewOne)
		if string(readFile(t, presencePath(dir))) != "garbage" {
			t.Fatal("the corrupt file was replaced")
		}
	})
	t.Run("a second enrolment while one runs", func(t *testing.T) {
		dir := t.TempDir()
		unlock, err := lockEnrolment(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer unlock()
		if tty, _ := presRefused(t, dir, cascade.KindConflict, presTestPass, presTestPass); tty.calls != 0 {
			t.Fatal("a locked enrolment prompted")
		}
	})
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// savedThenFails publishes the trust record and then reports a failure, as a
// sync error after the rename does.
type savedThenFails struct{ Backend }

func (b savedThenFails) Save(rec TrustRecord) error {
	if err := b.Backend.Save(rec); err != nil {
		return err
	}
	return errors.New("sync failed after publication")
}

func TestEnrollPresenceUnknownTrustStateRollsNothingBack(t *testing.T) {
	dir, _ := presEnrol(t, presTestPass)
	before := readFile(t, presencePath(dir))
	tty := newPresTTY(presTestPass, presTestNewOne, presTestNewOne)
	backend := &failAfterSave{Backend: NewFileBackend(dir)}
	_, err := enrollPresence(dir, tty.open, noEnv, backend, presClock(), &presAudit{})
	if !cascade.HasKind(err, cascade.KindIntegrity) || bytes.Equal(before, readFile(t, presencePath(dir))) {
		t.Fatalf("err=%v; want KindIntegrity and the new key left in place (nothing rolled back)", err)
	}
	kept, _ := filepath.Glob(presencePath(dir) + ".previous.*")
	if len(kept) != 1 || !bytes.Equal(before, readFile(t, kept[0])) || !strings.Contains(err.Error(), kept[0]) {
		t.Fatalf("kept=%v; want the old key kept under a unique name that the error names", kept)
	}
}

// failAfterSave loads normally until Save is called, then fails Save and Load.
type failAfterSave struct {
	Backend
	saved bool
}

func (b *failAfterSave) Save(TrustRecord) error { b.saved = true; return errors.New("write failed") }

func (b *failAfterSave) Load() (TrustRecord, bool, error) {
	if b.saved {
		return TrustRecord{}, false, errors.New("read failed")
	}
	return b.Backend.Load()
}

func TestEnrollPresenceTrustWriteFailures(t *testing.T) {
	t.Run("fresh: a failed trust write removes the new key", func(t *testing.T) {
		dir, aud := t.TempDir(), &presAudit{}
		_, err := enrollPresence(dir, newPresTTY(presTestPass, presTestPass).open, noEnv, saveFails{NewFileBackend(dir)}, presClock(), aud)
		if err == nil || presenceKeyExists(dir) || len(aud.Finished) != 1 || aud.Finished[0] == nil {
			t.Fatalf("err=%v key=%v finished=%v; want the error, no key and a failed audit outcome", err, presenceKeyExists(dir), aud.Finished)
		}
	})
	t.Run("rotation: a failed trust write restores the old key", func(t *testing.T) {
		dir, _ := presEnrol(t, presTestPass)
		before := readFile(t, presencePath(dir))
		tty := newPresTTY(presTestPass, presTestNewOne, presTestNewOne)
		if _, err := enrollPresence(dir, tty.open, noEnv, saveFails{NewFileBackend(dir)}, presClock(), &presAudit{}); err == nil {
			t.Fatal("rotation with a failing trust store succeeded")
		}
		if !bytes.Equal(before, readFile(t, presencePath(dir))) {
			t.Fatal("the rotated key was left in place although the trust record could not follow")
		}
	})
	t.Run("a trust record that was published despite the error is success", func(t *testing.T) {
		dir, aud := t.TempDir(), &presAudit{}
		fp, err := enrollPresence(dir, newPresTTY(presTestPass, presTestPass).open, noEnv, savedThenFails{NewFileBackend(dir)}, presClock(), aud)
		rec, ok, _ := NewFileBackend(dir).Load()
		if got, _, _ := PresenceFingerprint(dir); err != nil || got != fp || !ok || rec.FingerprintSHA256 != fp || len(aud.Finished) != 1 || aud.Finished[0] != nil {
			t.Fatalf("err=%v fp=%q on disk=%q trust=%+v finished=%v; want a consistent success", err, fp, got, rec, aud.Finished)
		}
	})
	t.Run("a rotation whose key changed after the snapshot refuses", func(t *testing.T) {
		dir, _ := presEnrol(t, presTestPass)
		cur, _ := readPresence(dir)
		stale := cur
		stale.Salt = base64.StdEncoding.EncodeToString(make([]byte, presenceSaltLen))
		_, priv, _ := ed25519.GenerateKey(rand.Reader)
		next, err := sealPresence([]byte(presTestNewOne), priv)
		if err != nil {
			t.Fatal(err)
		}
		before := readFile(t, presencePath(dir))
		if err := commitPresence(dir, next, stale, true, NewFileBackend(dir), presClock()); !cascade.HasKind(err, cascade.KindConflict) || !bytes.Equal(before, readFile(t, presencePath(dir))) {
			t.Fatalf("commit over a changed key = %v; want a conflict and an unchanged file", err)
		}
	})
}
