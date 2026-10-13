//go:build linux

// Purpose: hardening tests for presence.key: cost ceilings, file mode and
//
//	type, and a forged key whose seed and public half disagree.
//
// SPORT: internal/elevation presence tests/ADDED (P1-SEC-25).
package elevation

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// sealRaw seals plain under presTestPass with pub as the recorded public key and the matching AAD.
func sealRaw(t *testing.T, dir string, plain []byte, pub ed25519.PublicKey) {
	t.Helper()
	rewritePresence(t, dir, func(f *presenceFile) {
		f.Pub = base64.StdEncoding.EncodeToString(pub)
		key, err := f.derive([]byte(presTestPass))
		if err != nil {
			t.Fatal(err)
		}
		gcm, _ := presenceGCM(key)
		nonce, _ := base64.StdEncoding.DecodeString(f.Nonce)
		f.CT = base64.StdEncoding.EncodeToString(gcm.Seal(nil, nonce, plain, f.aad()))
	})
}

// hardeningCase corrupts an enrolled presence.key; Sign must then refuse with KindIntegrity.
type hardeningCase struct {
	name   string
	mutate func(t *testing.T, dir string)
	prompt bool // the refusal happens only after the prompt
}

func runHardening(t *testing.T, cases []hardeningCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir, _ := presEnrol(t, presTestPass)
			tc.mutate(t, dir)
			tty := newPresTTY(presTestPass)
			sig, err := newPresenceKeystore(dir, tty.open, noEnv).Sign([]byte("p"))
			if !cascade.HasKind(err, cascade.KindIntegrity) || len(sig) != 0 || (tty.calls == 1) != tc.prompt {
				t.Fatalf("err=%v sig=%d bytes prompts=%d; want KindIntegrity, no signature, prompted=%v", err, len(sig), tty.calls, tc.prompt)
			}
		})
	}
}

func TestKeystorePresenceHardening(t *testing.T) {
	runHardening(t, []hardeningCase{
		{"scrypt cost above the memory ceiling", func(t *testing.T, d string) {
			rewritePresence(t, d, func(f *presenceFile) { f.N, f.R, f.P = 1<<18, 16, 4 })
		}, false},
		{"scrypt cost far above every ceiling", func(t *testing.T, d string) {
			rewritePresence(t, d, func(f *presenceFile) { f.N, f.R, f.P = 1<<20, 32, 16 })
		}, false},
		{"work above the ceiling with memory inside it", func(t *testing.T, d string) {
			rewritePresence(t, d, func(f *presenceFile) { f.N, f.R, f.P = 1<<18, 8, 4 })
		}, false},
		{"larger than any valid key file", func(t *testing.T, d string) {
			if err := os.WriteFile(presencePath(d), make([]byte, presenceMaxFile+1), 0o600); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"a FIFO named presence.key", func(t *testing.T, d string) {
			if err := os.Remove(presencePath(d)); err != nil {
				t.Fatal(err)
			}
			if err := syscall.Mkfifo(presencePath(d), 0o600); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"mode 0644", func(_ *testing.T, d string) { _ = os.Chmod(presencePath(d), 0o644) }, false},
		{"symlink to the real file", func(t *testing.T, d string) {
			target := presencePath(d) + ".target"
			if err := os.Rename(presencePath(d), target); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, presencePath(d)); err != nil {
				t.Fatal(err)
			}
		}, false},
	})
}

func TestKeystorePresenceForgedKeys(t *testing.T) {
	runHardening(t, []hardeningCase{
		{"seed matches pub_b64 but the stored public half belongs to another key", func(t *testing.T, d string) {
			f, _ := readPresence(d)
			pubA, _ := base64.StdEncoding.DecodeString(f.Pub)
			pubB, privB, _ := ed25519.GenerateKey(rand.Reader)
			sealRaw(t, d, append(append([]byte(nil), privB[:ed25519.SeedSize]...), pubA...), pubB)
		}, true},
		{"seed of one key with the public half of another", func(t *testing.T, d string) {
			f, _ := readPresence(d)
			pubA, _ := base64.StdEncoding.DecodeString(f.Pub)
			_, privB, _ := ed25519.GenerateKey(rand.Reader)
			sealRaw(t, d, append(append([]byte(nil), privB[:ed25519.SeedSize]...), pubA...), pubA)
		}, true},
	})
}

// swapOnSave publishes the trust record and then swaps presence.key for another valid key.
type swapOnSave struct {
	Backend
	dir string
}

func (b swapOnSave) Save(rec TrustRecord) error {
	if err := b.Backend.Save(rec); err != nil {
		return err
	}
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	other, err := sealPresence([]byte(presTestNewOne), priv)
	if err != nil {
		return err
	}
	return os.WriteFile(presencePath(b.dir), other.marshal(), 0o600)
}

// stuckRestore fails Save after turning presence.key into a non-empty directory, so no restore can remove it.
type stuckRestore struct {
	Backend
	dir string
}

func (b stuckRestore) Save(TrustRecord) error {
	p := presencePath(b.dir)
	if err := os.Remove(p); err != nil {
		return err
	}
	if err := os.Mkdir(p, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(p+"/x", []byte("x"), 0o600); err != nil {
		return err
	}
	return errors.New("disk full")
}

func TestEnrollPresencePostConditionsAndRestoreErrors(t *testing.T) {
	t.Run("a key swapped during the commit is reported, not trusted", func(t *testing.T) {
		dir, aud := t.TempDir(), &presAudit{}
		_, err := enrollPresence(dir, newPresTTY(presTestPass, presTestPass).open, noEnv, swapOnSave{NewFileBackend(dir), dir}, presClock(), aud)
		if !cascade.HasKind(err, cascade.KindIntegrity) || len(aud.Finished) != 1 || aud.Finished[0] == nil {
			t.Fatalf("err=%v finished=%v; want KindIntegrity and a failed audit outcome", err, aud.Finished)
		}
	})
	t.Run("a restore that fails is reported together with the trust error", func(t *testing.T) {
		dir := t.TempDir()
		_, err := enrollPresence(dir, newPresTTY(presTestPass, presTestPass).open, noEnv, stuckRestore{NewFileBackend(dir), dir}, presClock(), &presAudit{})
		if !cascade.HasKind(err, cascade.KindIntegrity) || err == nil || !strings.Contains(err.Error(), "could not be restored") || !strings.Contains(err.Error(), "disk full") {
			t.Fatalf("err=%v; want KindIntegrity naming both the restore failure and the trust error", err)
		}
	})
}

func TestEnrollPresencePassphraseLengthBoundary(t *testing.T) {
	short, ok := "elevenbytes", "twelve bytes"
	if len(short) != 11 || len(ok) != 12 {
		t.Fatalf("fixture lengths %d and %d, want 11 and 12", len(short), len(ok))
	}
	dir := t.TempDir()
	if _, err := enrollPresence(dir, newPresTTY(short, short).open, noEnv, NewFileBackend(dir), presClock(), &presAudit{}); !cascade.HasKind(err, cascade.KindInvalidInput) || presenceKeyExists(dir) {
		t.Fatalf("an 11-byte passphrase = %v key=%v; want KindInvalidInput and no key", err, presenceKeyExists(dir))
	}
	restore := SetPresenceCostForTest(presenceMinN)
	defer restore()
	if _, err := enrollPresence(dir, newPresTTY(ok, ok).open, noEnv, NewFileBackend(dir), presClock(), &presAudit{}); err != nil || !presenceKeyExists(dir) {
		t.Fatalf("a 12-byte passphrase = %v key=%v; want it accepted", err, presenceKeyExists(dir))
	}
}
