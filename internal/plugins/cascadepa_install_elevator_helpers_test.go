package plugins

// Purpose (this file): shared, untagged fixtures for both
//   cascadepa_install_elevator_test.go (the platform-agnostic refusal
//   cases: none of them depend on a real ELEVATION_REQUIRED nonce ever
//   being issued, so they run on every CI lane including Windows) and
//   cascadepa_install_elevator_posix_test.go (the one real, full
//   nonce-issue/local-auth-sign/attestation-verify round trip, which is
//   architecturally unreachable on Windows -- see that file's header).
//   Split out of cascadepa_install_elevator_test.go so the fixtures live
//   in a file with no `//go:build` tag and are visible to both.
// SPORT: internal/plugins:cascadepa-install-wiring (TEST) -- P1-E24-W5-S50-T4
//   (CI fix ci-fix12, mirroring cmd/cascade/backup_elevation.go's own
//   Windows tier-2 fix, P1-E19-W4-S42-T3).

import (
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
