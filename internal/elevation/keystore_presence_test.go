//go:build linux

// Purpose: same-package presence tests (scrypt cost and zeroing hooks); the
//
//	bridge at the bottom re-exports test doubles to elevation_test.
//
// SPORT: internal/elevation presence tests/ADDED (P1-SEC-25).
package elevation

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

const (
	presTestPass   = "correct horse battery"
	presTestWrong  = "wrong horse battery!"
	presTestNewOne = "second staple passphrase"
)

// scriptedPrompter answers ReadSecret from a fixed list and counts reads. It
// keeps every slice it hands out so a test can check they were zeroed.
type scriptedPrompter struct {
	answers []string
	handed  [][]byte
	reads   int
}

func (p *scriptedPrompter) ReadSecret(string) ([]byte, error) {
	p.reads++
	if len(p.answers) == 0 {
		return nil, errors.New("scripted prompter: no answer left")
	}
	out := []byte(p.answers[0])
	p.answers = p.answers[1:]
	p.handed = append(p.handed, out)
	return out, nil
}

// presTTY counts factory calls and hands out the same prompter each time.
type presTTY struct {
	p     *scriptedPrompter
	calls int
}

func (f *presTTY) open() (ttyPrompter, error) { f.calls++; return f.p, nil }

func newPresTTY(answers ...string) *presTTY { return &presTTY{p: &scriptedPrompter{answers: answers}} }

// presAudit is a PresenceAuditor that records what it was told.
type presAudit struct {
	BeginErr error
	Begun    []string
	Finished []error
}

func (a *presAudit) Begin(fp, kind string) (func(error), error) {
	if a.BeginErr != nil {
		return nil, a.BeginErr
	}
	a.Begun = append(a.Begun, kind+":"+fp)
	return func(err error) { a.Finished = append(a.Finished, err) }, nil
}

func noEnv(string) string { return "" }

func presClock() runtime.Clock { return runtime.NewFixedClock(time.Unix(1_700_000_000, 0)) }

// presEnrol enrols with pass and returns the data dir and fingerprint.
func presEnrol(t *testing.T, pass string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	restore := SetPresenceCostForTest(presenceMinN)
	t.Cleanup(restore)
	fp, err := enrollPresence(dir, newPresTTY(pass, pass).open, noEnv, NewFileBackend(dir), presClock(), &presAudit{})
	if err != nil {
		t.Fatalf("enrol: %v", err)
	}
	return dir, fp
}

func wantKind(t *testing.T, err error, kind cascade.Kind) {
	t.Helper()
	if got, ok := cascade.KindOf(err); !ok || got != kind {
		t.Fatalf("error kind = %v (ok=%v) err=%v, want %v", got, ok, err, kind)
	}
}

func TestPresenceWrongPassphraseRefuses(t *testing.T) {
	dir, _ := presEnrol(t, presTestPass)
	ks := newPresenceKeystore(dir, newPresTTY(presTestWrong).open, noEnv)
	sig, err := ks.Sign([]byte("payload"))
	wantKind(t, err, cascade.KindPermissionDenied)
	if err.Error() != ErrAuthFailed(nil).Error() {
		t.Fatalf("message = %q, want the local-authentication refusal", err)
	}
	if len(sig) != 0 {
		t.Fatalf("a wrong passphrase returned %d signature bytes", len(sig))
	}
	if sig, err = newPresenceKeystore(dir, newPresTTY(presTestPass).open, noEnv).Sign([]byte("payload")); err != nil || len(sig) != ed25519.SignatureSize {
		t.Fatalf("the right passphrase must sign (control): %v / %d bytes", err, len(sig))
	}
}

func TestPresenceNoInputRefusesBeforeTTY(t *testing.T) {
	dir, _ := presEnrol(t, presTestPass)
	tty := newPresTTY(presTestPass)
	ks := newPresenceKeystore(dir, tty.open, func(k string) string {
		if k == "CASCADE_NO_INPUT" {
			return "1"
		}
		return ""
	})
	sig, err := ks.Sign([]byte("payload"))
	if err == nil || err.Error() != ErrNoInput().Error() || len(sig) != 0 {
		t.Fatalf("Sign under CASCADE_NO_INPUT=1 = %v / %d bytes, want ErrNoInput", err, len(sig))
	}
	if tty.calls != 0 || tty.p.reads != 0 {
		t.Fatalf("terminal opened %d times, read %d times; want 0 and 0", tty.calls, tty.p.reads)
	}
	t.Run("enrolment refuses too", func(t *testing.T) {
		d := t.TempDir()
		tty := newPresTTY(presTestPass, presTestPass)
		_, err := enrollPresence(d, tty.open, func(string) string { return "1" }, NewFileBackend(d), presClock(), &presAudit{})
		if err == nil || err.Error() != ErrNoInput().Error() || tty.calls != 0 || presenceKeyExists(d) {
			t.Fatalf("enrolment under NO_INPUT: err=%v calls=%d exists=%v", err, tty.calls, presenceKeyExists(d))
		}
	})
	ctl := newPresTTY(presTestPass)
	if _, err := newPresenceKeystore(dir, ctl.open, noEnv).Sign([]byte("p")); err != nil || ctl.calls != 1 {
		t.Fatalf("control without NO_INPUT: err=%v calls=%d, want nil and 1", err, ctl.calls)
	}
}

// rewritePresence loads presence.key, applies edit and writes it back.
func rewritePresence(t *testing.T, dir string, edit func(*presenceFile)) {
	t.Helper()
	f, err := readPresence(dir)
	if err != nil {
		t.Fatal(err)
	}
	edit(&f)
	raw, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(presencePath(dir), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// forgeMismatch seals privY under the AAD of otherPub, so AES-GCM opens and
// only the public-key equality check can refuse.
func forgeMismatch(t *testing.T, dir string, other ed25519.PublicKey) {
	t.Helper()
	_, privY, _ := ed25519.GenerateKey(rand.Reader)
	rewritePresence(t, dir, func(f *presenceFile) {
		f.Pub = base64.StdEncoding.EncodeToString(other)
		key, err := f.derive([]byte(presTestPass))
		if err != nil {
			t.Fatal(err)
		}
		block, _ := aes.NewCipher(key)
		gcm, _ := cipher.NewGCM(block)
		nonce, _ := base64.StdEncoding.DecodeString(f.Nonce)
		f.CT = base64.StdEncoding.EncodeToString(gcm.Seal(nil, nonce, privY, f.aad()))
	})
}

func TestPresenceTamperedFileRefuses(t *testing.T) {
	otherPub, _, _ := ed25519.GenerateKey(rand.Reader)
	cases := []struct {
		name   string
		tamper func(*testing.T, string)
		kinds  []cascade.Kind
		noTTY  bool
	}{
		{"swapped pub_b64", func(t *testing.T, d string) {
			rewritePresence(t, d, func(f *presenceFile) { f.Pub = base64.StdEncoding.EncodeToString(otherPub) })
		}, []cascade.Kind{cascade.KindPermissionDenied}, false},
		{"pub differs from the sealed key but AAD matches", func(t *testing.T, d string) { forgeMismatch(t, d, otherPub) },
			[]cascade.Kind{cascade.KindIntegrity}, false},
		{"lowered n", func(t *testing.T, d string) {
			rewritePresence(t, d, func(f *presenceFile) { f.N = presenceMinN / 2 })
		}, []cascade.Kind{cascade.KindIntegrity}, true},
		{"flipped ciphertext byte", func(t *testing.T, d string) {
			rewritePresence(t, d, func(f *presenceFile) {
				ct, _ := base64.StdEncoding.DecodeString(f.CT)
				ct[3] ^= 0x01
				f.CT = base64.StdEncoding.EncodeToString(ct)
			})
		}, []cascade.Kind{cascade.KindPermissionDenied}, false},
		{"unknown version", func(t *testing.T, d string) { rewritePresence(t, d, func(f *presenceFile) { f.V = 2 }) },
			[]cascade.Kind{cascade.KindIntegrity}, true},
		{"low r", func(t *testing.T, d string) { rewritePresence(t, d, func(f *presenceFile) { f.R = 1 }) },
			[]cascade.Kind{cascade.KindIntegrity}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir, _ := presEnrol(t, presTestPass)
			if _, err := newPresenceKeystore(dir, newPresTTY(presTestPass).open, noEnv).Sign([]byte("p")); err != nil {
				t.Fatalf("untampered control must sign: %v", err)
			}
			tc.tamper(t, dir)
			tty := newPresTTY(presTestPass)
			sig, err := newPresenceKeystore(dir, tty.open, noEnv).Sign([]byte("p"))
			if err == nil || len(sig) != 0 {
				t.Fatalf("tampered file signed: err=%v sig=%d bytes", err, len(sig))
			}
			if got, _ := cascade.KindOf(err); got != tc.kinds[0] {
				t.Fatalf("kind = %v (%v), want %v", got, err, tc.kinds[0])
			}
			if tc.noTTY && tty.calls != 0 {
				t.Fatalf("a structurally invalid file must refuse before the prompt; terminal opened %d times", tty.calls)
			}
		})
	}
}

func TestPresenceZeroesAfterSign(t *testing.T) {
	dir, _ := presEnrol(t, presTestPass)
	var calls int
	var seen [][]byte
	presenceWipeHook = func(pass, key, plain []byte) { calls++; seen = [][]byte{pass, key, plain} }
	t.Cleanup(func() { presenceWipeHook = nil })
	tty := newPresTTY(presTestPass)
	if _, err := newPresenceKeystore(dir, tty.open, noEnv).Sign([]byte("payload")); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("zero hook ran %d times, want 1", calls)
	}
	for i, name := range []string{"passphrase", "derived key", "plaintext private key"} {
		if len(seen[i]) == 0 {
			t.Fatalf("%s buffer was empty: the hook observed nothing", name)
		}
		if !bytes.Equal(seen[i], make([]byte, len(seen[i]))) {
			t.Fatalf("%s buffer is not zero after Sign", name)
		}
	}
	if len(seen[2]) != ed25519.PrivateKeySize || len(seen[1]) != 32 {
		t.Fatalf("hook saw lengths key=%d plain=%d", len(seen[1]), len(seen[2]))
	}
	if h := tty.p.handed[0]; !bytes.Equal(h, make([]byte, len(h))) {
		t.Fatal("the buffer the prompter handed out was not zeroed")
	}
}

// Bridge for the external test package (internal/rpc imports this package).

// ScriptedPrompter, PresenceAudit and PresencePrompter re-export the test
// doubles and the unexported terminal interface for elevation_test.
type (
	ScriptedPrompter = scriptedPrompter
	PresenceAudit    = presAudit
	PresencePrompter = ttyPrompter
)

// NewScriptedPrompter builds a prompter that answers in order.
func NewScriptedPrompter(answers ...string) *ScriptedPrompter {
	return &scriptedPrompter{answers: answers}
}

// Reads reports how many secrets were requested.
func (p *scriptedPrompter) Reads() int { return p.reads }

// NewPresenceKeystoreForTest exposes newPresenceKeystore.
var NewPresenceKeystoreForTest = newPresenceKeystore

// EnrollPresenceForTest exposes enrollPresence.
var EnrollPresenceForTest = enrollPresence

// NewProductionPresenceForTest builds the keystore with the real terminal opener.
func NewProductionPresenceForTest(dir string) ElevationKeystore {
	return newPresenceKeystore(dir, openControllingTTY, os.Getenv)
}

// SetPresenceCostForTest lowers the enrolment scrypt cost (never below the read floor).
func SetPresenceCostForTest(n int) (restore func()) {
	old := presenceEnrolN
	presenceEnrolN = n
	return func() { presenceEnrolN = old }
}
