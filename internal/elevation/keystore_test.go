// Purpose: table-driven tests for ElevationKeystore's contract, exercised
//
//	against an in-process fake (Art.1: fakes live ONLY in _test.go).
//	These tests prove the INTERFACE's failure semantics — every real
//	platform backend (keystore_darwin.go, keystore_linux.go,
//	keystore_windows.go) is expected to honor the same fail-closed shape
//	this fake enforces, but this file never claims to exercise the real
//	Security.framework/PAM calls themselves (that is
//	keystore_darwin_integration_test.go / keystore_linux_integration_test.go,
//	Art.2's real-counterpart obligation).
//
// SPORT: internal/elevation keystore tests/ADDED (P1-E04-W1-S07-T6).
package elevation

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// fakeKeystore is the in-process ElevationKeystore fake this ticket's
// contract asks for (Art.1: exists only under _test.go). authFails, when
// true, makes Sign behave exactly like a real backend whose local
// authentication step was declined/canceled — it never reaches the
// signing step.
type fakeKeystore struct {
	available   bool
	generateErr error
	pub         ed25519.PublicKey
	priv        ed25519.PrivateKey
	authFails   bool
	tier        StorageTier
}

func (f *fakeKeystore) IsAvailable() bool { return f.available }
func (f *fakeKeystore) Tier() StorageTier { return f.tier }

func (f *fakeKeystore) GenerateKey() error {
	if f.generateErr != nil {
		return f.generateErr
	}
	if f.pub != nil {
		return nil // idempotent, matches every real backend's contract
	}
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "fakeKeystore: generate key")
	}
	f.pub, f.priv = pub, priv
	f.tier = TierOSKeychain
	return nil
}

func (f *fakeKeystore) PubKeyB64() (string, error) {
	if f.pub == nil {
		return "", ErrHelperNotEnrolled()
	}
	return base64.StdEncoding.EncodeToString(f.pub), nil
}

func (f *fakeKeystore) Sign(payload []byte) ([]byte, error) {
	if f.authFails {
		return nil, ErrAuthFailed(nil)
	}
	if f.priv == nil {
		return nil, ErrHelperNotEnrolled()
	}
	return ed25519.Sign(f.priv, payload), nil
}

func TestKeystore_GenerateKey_Success(t *testing.T) {
	f := &fakeKeystore{available: true}
	if err := f.GenerateKey(); err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	if f.pub == nil {
		t.Fatal("GenerateKey did not populate a public key")
	}
}

func TestKeystore_GenerateKey_Idempotent(t *testing.T) {
	f := &fakeKeystore{available: true}
	if err := f.GenerateKey(); err != nil {
		t.Fatalf("first GenerateKey: %v", err)
	}
	first := append(ed25519.PublicKey{}, f.pub...)
	if err := f.GenerateKey(); err != nil {
		t.Fatalf("second GenerateKey: %v", err)
	}
	if string(first) != string(f.pub) {
		t.Fatal("GenerateKey regenerated an already-enrolled key")
	}
}

func TestKeystore_GenerateKey_Failure(t *testing.T) {
	f := &fakeKeystore{available: false, generateErr: ErrKeystoreUnavailable(errors.New("no keychain daemon"))}
	err := f.GenerateKey()
	if err == nil {
		t.Fatal("GenerateKey must fail when the backing store is unavailable")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindUnavailable {
		t.Errorf("kind = %v (ok=%v), want KindUnavailable", kind, ok)
	}
}

func TestKeystore_PubKeyB64_MissingKey(t *testing.T) {
	f := &fakeKeystore{available: true}
	_, err := f.PubKeyB64()
	if err == nil {
		t.Fatal("PubKeyB64 before GenerateKey must fail closed, not return a zero-value key")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindNotFound {
		t.Errorf("kind = %v (ok=%v), want KindNotFound", kind, ok)
	}
}

func TestKeystore_Sign_WithoutAuth_MissingEnrollment(t *testing.T) {
	f := &fakeKeystore{available: true}
	_, err := f.Sign([]byte("payload"))
	if err == nil {
		t.Fatal("Sign with no enrolled key must fail closed")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindNotFound {
		t.Errorf("kind = %v (ok=%v), want KindNotFound", kind, ok)
	}
}

func TestKeystore_Sign_AuthFailure(t *testing.T) {
	f := &fakeKeystore{available: true, authFails: true}
	if err := f.GenerateKey(); err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	_, err := f.Sign([]byte("payload"))
	if err == nil {
		t.Fatal("Sign must fail when local authentication fails — never fall through to a signature")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindPermissionDenied {
		t.Errorf("kind = %v (ok=%v), want KindPermissionDenied", kind, ok)
	}
}

func TestKeystore_Sign_Success(t *testing.T) {
	f := &fakeKeystore{available: true}
	if err := f.GenerateKey(); err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	sig, err := f.Sign([]byte("payload"))
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if !ed25519.Verify(f.pub, []byte("payload"), sig) {
		t.Fatal("Sign produced a signature that does not verify against the enrolled public key")
	}
}

func TestKeystore_IsAvailable(t *testing.T) {
	if (&fakeKeystore{available: true}).IsAvailable() != true {
		t.Error("IsAvailable(true) reported false")
	}
	if (&fakeKeystore{available: false}).IsAvailable() != false {
		t.Error("IsAvailable(false) reported true")
	}
}

// TestKeystore_Errors_AreTaxonomyKinds asserts every constructor in
// keystore.go returns a valid, correctly-kinded taxonomy error — the
// frozen 14-kind enumeration (pkg/cascade.Kind) is the ONLY vocabulary
// this package may use (hard requirement: no invented kind).
func TestKeystore_Errors_AreTaxonomyKinds(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want cascade.Kind
	}{
		{"ErrHelperNotEnrolled", ErrHelperNotEnrolled(), cascade.KindNotFound},
		{"ErrAuthFailed(nil)", ErrAuthFailed(nil), cascade.KindPermissionDenied},
		{"ErrAuthFailed(cause)", ErrAuthFailed(errors.New("policy evaluation error")), cascade.KindPermissionDenied},
		{"ErrAlreadyEnrolled", ErrAlreadyEnrolled(), cascade.KindConflict},
		{"ErrWindowsTier2", ErrWindowsTier2(), cascade.KindUnsupported},
		{"ErrKeystoreUnavailable(nil)", ErrKeystoreUnavailable(nil), cascade.KindUnavailable},
		{"ErrKeystoreUnavailable(cause)", ErrKeystoreUnavailable(errors.New("no keychain daemon")), cascade.KindUnavailable},
		{"ErrNoInput", ErrNoInput(), cascade.KindPermissionDenied},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind, ok := cascade.KindOf(tc.err)
			if !ok || kind != tc.want {
				t.Errorf("kind = %v (ok=%v), want %v", kind, ok, tc.want)
			}
			if !kind.Valid() {
				t.Errorf("%s produced an invalid Kind", tc.name)
			}
		})
	}
}
