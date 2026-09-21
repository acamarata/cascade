package plugins

// Purpose (this file): unit coverage for cascadepa_install_elevator.go's
//   real attestation flow -- NO REAL KEYCHAIN and NO REAL HOME (Art.7.1,
//   matching cascadepa_bridge_wiring_test.go's own hard rule): every
//   ElevationKeystore here is an in-memory fake, but the surrounding
//   orchestration (rpc.NonceLedger, rpc.ElevationMiddleware,
//   rpc.VerifyAttestation, elevation.ElevationTrustStore over a REAL
//   TempDir-backed elevation.NewFileBackend) is the genuine production
//   code, exercised end to end.
// SPORT: internal/plugins:cascadepa-install-wiring (TEST) -- P1-E24-W5-S50-T4.

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"testing"

	"github.com/acamarata/cascade/internal/elevation"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/plugin"
	"github.com/acamarata/cascade/plugins/cascade-pa/install"
)

// fakeElevationKeystore is an in-memory ElevationKeystore: real Ed25519
// signing, a fake "local auth" gate (authFail) standing in for a real
// Keychain/PAM prompt.
type fakeElevationKeystore struct {
	pub      ed25519.PublicKey
	priv     ed25519.PrivateKey
	tier     elevation.StorageTier
	authFail bool
}

func newFakeElevationKeystore(t *testing.T) *fakeElevationKeystore {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate fake elevation key: %v", err)
	}
	return &fakeElevationKeystore{pub: pub, priv: priv, tier: elevation.TierOSKeychain}
}

func (f *fakeElevationKeystore) GenerateKey() error { return nil }
func (f *fakeElevationKeystore) PubKeyB64() (string, error) {
	return base64.StdEncoding.EncodeToString(f.pub), nil
}
func (f *fakeElevationKeystore) Sign(payload []byte) ([]byte, error) {
	if f.authFail {
		return nil, elevation.ErrAuthFailed(errors.New("fake local auth declined"))
	}
	return ed25519.Sign(f.priv, payload), nil
}
func (f *fakeElevationKeystore) IsAvailable() bool           { return true }
func (f *fakeElevationKeystore) Tier() elevation.StorageTier { return f.tier }

// newTestInstallElevator builds an installElevator over a real TempDir
// (real file I/O for the trust backend, never a real Keychain) with ks
// substituted for elevation.SelectKeystore's real platform probe.
func newTestInstallElevator(t *testing.T, ks elevation.ElevationKeystore, getenv func(string) string) (*installElevator, string) {
	t.Helper()
	dir := t.TempDir()
	e := newInstallElevator(func() (runtime.PathProvider, error) { return tempDataPathProvider{dir: dir}, nil },
		testkit.NewFrozenClock(fixedInstallTestTime), getenv)
	e.keystore = func(string) elevation.ElevationKeystore { return ks }
	return e, dir
}

// enroll pre-enrolls ks's public key in the real file-backed trust store
// at dir, mirroring `cascade elevate-helper --enroll`'s real effect.
func enroll(t *testing.T, ks elevation.ElevationKeystore, dir string) {
	t.Helper()
	pub, err := ks.PubKeyB64()
	if err != nil {
		t.Fatalf("PubKeyB64: %v", err)
	}
	store := elevation.NewElevationTrustStore(elevation.NewFileBackend(dir), testkit.NewFrozenClock(fixedInstallTestTime))
	if _, err := store.Enroll(pub); err != nil {
		t.Fatalf("Enroll: %v", err)
	}
}

func testElevationRequest() install.ElevationRequest {
	return install.ElevationRequest{PluginID: "grant-expand-plugin", Runtime: plugin.RuntimeWasm}
}

func TestInstallElevator_NoInputRefusesBeforeTouchingKeystore(t *testing.T) {
	touched := false
	ks := newFakeElevationKeystore(t)
	e, _ := newTestInstallElevator(t, ks, func(string) string { return "1" })
	e.keystore = func(string) elevation.ElevationKeystore { touched = true; return ks }
	res, err := e.Elevate(context.Background(), testElevationRequest())
	if err == nil {
		t.Fatal("Elevate: err = nil, want a CASCADE_NO_INPUT refusal")
	}
	if res.Approved {
		t.Fatal("Approved = true under CASCADE_NO_INPUT=1")
	}
	if touched {
		t.Fatal("the keystore was constructed under CASCADE_NO_INPUT=1 -- must refuse before any auth prompt could fire")
	}
}

func TestInstallElevator_WindowsTier2Refuses(t *testing.T) {
	ks := newFakeElevationKeystore(t)
	ks.tier = elevation.TierWindowsTier2
	e, _ := newTestInstallElevator(t, ks, func(string) string { return "" })
	res, err := e.Elevate(context.Background(), testElevationRequest())
	if err == nil {
		t.Fatal("Elevate: err = nil, want the Windows tier-2 refusal")
	}
	if res.Approved {
		t.Fatal("Approved = true on a Windows tier-2 keystore")
	}
}

func TestInstallElevator_UnenrolledHostRefuses(t *testing.T) {
	ks := newFakeElevationKeystore(t)
	e, _ := newTestInstallElevator(t, ks, func(string) string { return "" })
	// No enroll() call: GetPubKey must fail closed.
	res, err := e.Elevate(context.Background(), testElevationRequest())
	if err == nil {
		t.Fatal("Elevate: err = nil, want an unenrolled-host refusal")
	}
	if res.Approved {
		t.Fatal("Approved = true with no enrolled trust record")
	}
}

func TestInstallElevator_EnrolledAndSignedApproves(t *testing.T) {
	ks := newFakeElevationKeystore(t)
	e, dir := newTestInstallElevator(t, ks, func(string) string { return "" })
	enroll(t, ks, dir)

	res, err := e.Elevate(context.Background(), testElevationRequest())
	if err != nil {
		t.Fatalf("Elevate: %v", err)
	}
	if !res.Approved {
		t.Fatal("Approved = false after a real nonce-issue/local-auth-sign/attestation-verify round trip succeeded")
	}
	if !res.Witness.Valid() {
		t.Fatal("Witness.Valid() = false on a genuinely approved elevation -- round-1 CR fix item 9: the retry must carry a real witness")
	}
}

// TestInstallElevator_AuthFailureRefuses is the mutation-adjacent case: a
// real local-auth decline (Sign returning ErrAuthFailed) must never read
// as Approved:true -- the fail-closed contract this file's header
// promises independent of the daemon's own un-wired ElevationMiddleware
// gap.
func TestInstallElevator_AuthFailureRefuses(t *testing.T) {
	ks := newFakeElevationKeystore(t)
	ks.authFail = true
	e, dir := newTestInstallElevator(t, ks, func(string) string { return "" })
	enroll(t, ks, dir)

	res, err := e.Elevate(context.Background(), testElevationRequest())
	if err == nil {
		t.Fatal("Elevate: err = nil, want the real local-auth failure propagated")
	}
	if res.Approved {
		t.Fatal("Approved = true despite a failed local-auth signature")
	}
	if res.Witness.Valid() {
		t.Fatal("Witness.Valid() = true on a refused elevation, want the zero witness")
	}
}

// TestInstallElevator_WrongEnrolledKeyRefuses proves VerifyAttestation's
// own signature check is genuinely exercised: a keystore that signs with a
// DIFFERENT key than the one enrolled in the trust store must be refused,
// never approved on a mismatched signature.
func TestInstallElevator_WrongEnrolledKeyRefuses(t *testing.T) {
	signer := newFakeElevationKeystore(t)
	enrolledOnly := newFakeElevationKeystore(t) // a different keypair, enrolled instead of signer's
	e, dir := newTestInstallElevator(t, signer, func(string) string { return "" })
	enroll(t, enrolledOnly, dir)

	res, err := e.Elevate(context.Background(), testElevationRequest())
	if err == nil {
		t.Fatal("Elevate: err = nil, want a pubkey-fingerprint mismatch refusal")
	}
	if res.Approved {
		t.Fatal("Approved = true with a signature from an unenrolled key")
	}
}

// TestInstallElevator_BuildKeystoreDefault_UsesRealSelectKeystore drives
// buildKeystore's REAL default (e.keystore == nil): elevation.SelectKeystore
// only probes fileKeyExists + IsAvailable, no prompt, no write, so it is
// safe to call directly on a TempDir (round-1 CR fix item 10).
func TestInstallElevator_BuildKeystoreDefault_UsesRealSelectKeystore(t *testing.T) {
	dir := t.TempDir()
	e := newInstallElevator(func() (runtime.PathProvider, error) { return tempDataPathProvider{dir: dir}, nil },
		testkit.NewFrozenClock(fixedInstallTestTime), func(string) string { return "" })
	ks := e.buildKeystore(dir)
	if ks == nil {
		t.Fatal("buildKeystore(dir) = nil, want the real elevation.SelectKeystore result")
	}
	if ks.Tier() == "" {
		t.Error("Tier() = \"\", want a real StorageTier name from the platform probe")
	}
}

// TestInstallElevator_BuildTrustBackendDefault_UsesRealFileBackend drives
// buildTrustBackend's REAL default (e.trustBackend == nil): a real, empty
// (unenrolled) file-backed trust store over a TempDir.
func TestInstallElevator_BuildTrustBackendDefault_UsesRealFileBackend(t *testing.T) {
	dir := t.TempDir()
	e := newInstallElevator(func() (runtime.PathProvider, error) { return tempDataPathProvider{dir: dir}, nil },
		testkit.NewFrozenClock(fixedInstallTestTime), func(string) string { return "" })
	backend := e.buildTrustBackend(dir)
	if backend == nil {
		t.Fatal("buildTrustBackend(dir) = nil, want the real elevation.NewFileBackend result")
	}
	store := elevation.NewElevationTrustStore(backend, testkit.NewFrozenClock(fixedInstallTestTime))
	if _, err := store.GetPubKey(); err == nil {
		t.Fatal("GetPubKey on a fresh, unenrolled real file backend: err = nil, want the not-found refusal")
	}
}
