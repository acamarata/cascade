//go:build darwin && cgo

// Purpose: unit tests for darwinKeystore's Go-side control flow
//
//	(idempotency, error-code mapping, zeroing), using fakeDarwinBridge
//	(Art.1: this file only) instead of the real Security.framework/
//	LocalAuthentication calls. This does NOT satisfy Art.2's real-
//	counterpart obligation by itself — see keystore_darwin_integration_
//	test.go and testdata/README.md for what actually exercises the real
//	frameworks, and this ticket's journal for the honest boundary
//	between the two.
//
// SPORT: internal/elevation darwin unit tests/ADDED (P1-E04-W1-S07-T6).
package elevation

import (
	"crypto/ed25519"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// fakeDarwinBridge is an in-process darwinBridge fake (Art.1: _test.go
// only). It never touches the real Keychain or LocalAuthentication.
type fakeDarwinBridge struct {
	available    bool
	store        map[string][]byte
	storeFailAcc string // account name whose keychainStore call fails
	signN        int    // overrides signingKeyLoad's returned n when nonzero/negative sentinel set
	signSet      bool
}

func newFakeDarwinBridge() *fakeDarwinBridge {
	return &fakeDarwinBridge{available: true, store: map[string][]byte{}}
}

func (f *fakeDarwinBridge) laProbe() bool { return f.available }

func (f *fakeDarwinBridge) keychainStore(account string, data []byte, _ bool) int {
	if account == f.storeFailAcc {
		return -50 // arbitrary non-zero OSStatus
	}
	cp := append([]byte(nil), data...)
	f.store[account] = cp
	return 0
}

func (f *fakeDarwinBridge) keychainLoadPlain(account string, capacity int) ([]byte, bool) {
	v, ok := f.store[account]
	if !ok || len(v) != capacity {
		return nil, false
	}
	return append([]byte(nil), v...), true
}

func (f *fakeDarwinBridge) signingKeyLoad(_ string, capacity int) ([]byte, int) {
	if f.signSet {
		return make([]byte, capacity), f.signN
	}
	v, ok := f.store[darwinPrivAcct]
	if !ok {
		return make([]byte, capacity), -3
	}
	return append([]byte(nil), v...), len(v)
}

// TestNewKeystore_ConstructsWithRealBridge proves NewKeystore wires up the
// real cgoBridge (never a fake) — it does not call any method on the
// result, since IsAvailable/GenerateKey/Sign here would touch the real
// Keychain/LocalAuthentication (see keystore_darwin_integration_test.go
// for that).
func TestNewKeystore_ConstructsWithRealBridge(t *testing.T) {
	ks, ok := NewKeystore().(darwinKeystore)
	if !ok {
		t.Fatalf("NewKeystore() returned %T, want darwinKeystore", NewKeystore())
	}
	if _, ok := ks.bridge.(cgoBridge); !ok {
		t.Errorf("NewKeystore() bridge = %T, want cgoBridge", ks.bridge)
	}
}

// TestCGOBridge_LaProbe_Real calls the REAL cascade_la_probe against this
// host's actual LocalAuthentication framework. This is safe to run
// unattended: canEvaluatePolicy is a read-only capability check — it never
// triggers a biometric/passcode UI prompt and never touches the Keychain
// — unlike keychainStore/keychainLoadPlain/signingKeyLoad, which either
// write real Keychain data or block on an interactive prompt and are
// deliberately left to keystore_darwin_integration_test.go instead. This
// is the one real Security/LocalAuthentication-framework call this
// ticket's non-integration suite exercises for real.
func TestCGOBridge_LaProbe_Real(_ *testing.T) {
	// No assertion on the boolean itself (this sandbox may or may not have
	// a passcode/biometrics configured) — the assertion is that the real
	// CGO call executes and returns without panicking or hanging.
	_ = cgoBridge{}.laProbe()
}

func TestDarwinKeystore_IsAvailable(t *testing.T) {
	ks := darwinKeystore{bridge: &fakeDarwinBridge{available: true}}
	if !ks.IsAvailable() {
		t.Error("IsAvailable = false, want true")
	}
	ks = darwinKeystore{bridge: &fakeDarwinBridge{available: false}}
	if ks.IsAvailable() {
		t.Error("IsAvailable = true, want false")
	}
}

func TestDarwinKeystore_Tier(t *testing.T) {
	bridge := newFakeDarwinBridge()
	ks := darwinKeystore{bridge: bridge}
	if ks.Tier() != TierUnavailable {
		t.Errorf("Tier() before enrollment = %v, want TierUnavailable", ks.Tier())
	}
	if err := ks.GenerateKey(); err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	if ks.Tier() != TierOSKeychain {
		t.Errorf("Tier() after enrollment = %v, want TierOSKeychain", ks.Tier())
	}
}

func TestDarwinKeystore_GenerateKey_Idempotent(t *testing.T) {
	bridge := newFakeDarwinBridge()
	ks := darwinKeystore{bridge: bridge}
	if err := ks.GenerateKey(); err != nil {
		t.Fatalf("first GenerateKey: %v", err)
	}
	first := append([]byte(nil), bridge.store[darwinPubAcct]...)
	if err := ks.GenerateKey(); err != nil {
		t.Fatalf("second GenerateKey: %v", err)
	}
	if string(first) != string(bridge.store[darwinPubAcct]) {
		t.Error("GenerateKey regenerated an already-enrolled key")
	}
}

func TestDarwinKeystore_GenerateKey_PrivateStoreFailure(t *testing.T) {
	bridge := newFakeDarwinBridge()
	bridge.storeFailAcc = darwinPrivAcct
	ks := darwinKeystore{bridge: bridge}
	err := ks.GenerateKey()
	if err == nil {
		t.Fatal("GenerateKey must fail when the private key cannot be stored")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindUnavailable {
		t.Errorf("kind = %v (ok=%v), want KindUnavailable", kind, ok)
	}
	if _, ok := bridge.store[darwinPubAcct]; ok {
		t.Error("public key must not be stored when the private key store failed")
	}
}

func TestDarwinKeystore_GenerateKey_PublicStoreFailure(t *testing.T) {
	bridge := newFakeDarwinBridge()
	bridge.storeFailAcc = darwinPubAcct
	ks := darwinKeystore{bridge: bridge}
	err := ks.GenerateKey()
	if err == nil {
		t.Fatal("GenerateKey must fail when the public key cannot be stored")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindUnavailable {
		t.Errorf("kind = %v (ok=%v), want KindUnavailable", kind, ok)
	}
}

func TestDarwinKeystore_PubKeyB64_MissingKey(t *testing.T) {
	ks := darwinKeystore{bridge: newFakeDarwinBridge()}
	_, err := ks.PubKeyB64()
	if err == nil {
		t.Fatal("PubKeyB64 before GenerateKey must fail")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindNotFound {
		t.Errorf("kind = %v (ok=%v), want KindNotFound", kind, ok)
	}
}

func TestDarwinKeystore_PubKeyB64_Success(t *testing.T) {
	bridge := newFakeDarwinBridge()
	ks := darwinKeystore{bridge: bridge}
	if err := ks.GenerateKey(); err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	pub, err := ks.PubKeyB64()
	if err != nil {
		t.Fatalf("PubKeyB64: %v", err)
	}
	if pub == "" {
		t.Error("PubKeyB64 returned empty string after enrollment")
	}
}

func TestDarwinKeystore_Sign_Success(t *testing.T) {
	bridge := newFakeDarwinBridge()
	ks := darwinKeystore{bridge: bridge}
	if err := ks.GenerateKey(); err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	sig, err := ks.Sign([]byte("payload"))
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	pub := ed25519.PublicKey(bridge.store[darwinPubAcct])
	if !ed25519.Verify(pub, []byte("payload"), sig) {
		t.Fatal("Sign produced a signature that does not verify")
	}
}

func TestDarwinKeystore_Sign_ErrorMapping(t *testing.T) {
	cases := []struct {
		name string
		n    int
		want cascade.Kind
	}{
		{"policy-unavailable", -1, cascade.KindUnavailable},
		{"auth-failed", -2, cascade.KindPermissionDenied},
		{"not-enrolled", -3, cascade.KindNotFound},
		{"unexpected-length", ed25519.PrivateKeySize - 1, cascade.KindIntegrity},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bridge := &fakeDarwinBridge{available: true, store: map[string][]byte{}, signSet: true, signN: tc.n}
			ks := darwinKeystore{bridge: bridge}
			_, err := ks.Sign([]byte("payload"))
			if err == nil {
				t.Fatal("Sign must fail")
			}
			if kind, ok := cascade.KindOf(err); !ok || kind != tc.want {
				t.Errorf("kind = %v (ok=%v), want %v", kind, ok, tc.want)
			}
		})
	}
}

func TestZero(t *testing.T) {
	b := []byte{1, 2, 3, 4}
	zero(b)
	for i, v := range b {
		if v != 0 {
			t.Errorf("zero() left b[%d] = %d, want 0", i, v)
		}
	}
}

// failingReader is an io.Reader that always errors, used to drive
// GenerateKey's ed25519.GenerateKey error-mapping branch (there is no way
// to make crypto/rand.Reader itself fail on a healthy host).
type failingReader struct{}

func (failingReader) Read([]byte) (int, error) {
	return 0, cascade.Newf(cascade.KindUnavailable, "elevation: simulated entropy failure")
}

func TestDarwinKeystore_GenerateKey_RandFailure(t *testing.T) {
	orig := edKeyRandSource
	edKeyRandSource = failingReader{}
	defer func() { edKeyRandSource = orig }()

	ks := darwinKeystore{bridge: newFakeDarwinBridge()}
	err := ks.GenerateKey()
	if err == nil {
		t.Fatal("GenerateKey must fail when the entropy source errors")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindUnavailable {
		t.Errorf("kind = %v (ok=%v), want KindUnavailable", kind, ok)
	}
}

// TestCGOBridge_KeychainLoadPlain_NotFound calls the REAL
// cascade_keychain_load_plain against this host's actual Keychain, but
// only the read-only "no such item" path: it queries an account name that
// is never written by this package (nor by any real Cascade install) so
// the call cannot return real key material, never writes anything, and
// never triggers a LocalAuthentication prompt (unprotected reads do not
// require device-owner presence). This exercises cgoBridge.keychainLoadPlain
// for real without touching any item this developer's Keychain actually
// holds. The store/signing bridge methods stay untested for real per this
// ticket's constraint (they would write real data or block on an
// interactive auth prompt).
func TestCGOBridge_KeychainLoadPlain_NotFound(t *testing.T) {
	buf, ok := cgoBridge{}.keychainLoadPlain("cascade-coverage-probe-account-never-enrolled", ed25519.PublicKeySize)
	if ok {
		t.Fatalf("keychainLoadPlain found an item for a probe account that should never exist (len=%d)", len(buf))
	}
	if buf != nil {
		t.Errorf("keychainLoadPlain returned non-nil buf on not-found, want nil")
	}
}
