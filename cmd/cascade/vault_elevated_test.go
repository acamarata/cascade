package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/elevation"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestVaultCLIGetRequiresElevation is the acceptance criterion: without an
// elevation session, get and rotate emit ELEVATION_REQUIRED as a typed
// error (never a panic) and read nothing.
func TestVaultCLIGetRequiresElevation(t *testing.T) {
	deps := testVaultDeps(t, okGate{}, map[string]string{"CASCADE_NO_INPUT": "1"})
	if _, _, err := runVault(t, deps, "s3cr3t\n", "set", "TOKEN"); err != nil {
		t.Fatalf("set: %v", err)
	}
	deps.Gate = refusingGate{}

	stdout, stderr, err := runVault(t, deps, "", "get", "TOKEN")
	if !isCLIKind(err, cascade.KindElevationRequired) {
		t.Fatalf("get without elevation = %v, want ELEVATION_REQUIRED", err)
	}
	if cascade.KindElevationRequired.ExitCode() != cascade.ExitElevationRequired {
		t.Fatal("the refusal does not map to the elevation-required exit status")
	}
	if strings.Contains(stdout+stderr+err.Error(), "s3cr3t") {
		t.Fatal("the refusal leaked the secret value")
	}

	stdout, stderr, err = runVault(t, deps, "new\n", "rotate", "TOKEN")
	if !isCLIKind(err, cascade.KindElevationRequired) {
		t.Fatalf("rotate without elevation = %v, want ELEVATION_REQUIRED", err)
	}
	if strings.Contains(stdout+stderr, "s3cr3t") {
		t.Fatal("the rotate refusal leaked the stored value")
	}

	// The refused rotate must not have written anything.
	deps.Gate = okGate{}
	stdout, _, err = runVault(t, deps, "", "get", "TOKEN")
	if err != nil {
		t.Fatalf("get after a refused rotate: %v", err)
	}
	if stdout != "s3cr3t" {
		t.Fatalf("the refused rotate changed the stored value to %q", stdout)
	}
}

func TestVaultCLIRotate(t *testing.T) {
	deps := testVaultDeps(t, okGate{}, map[string]string{"CASCADE_NO_INPUT": "1"})
	if _, _, err := runVault(t, deps, "old\n", "set", "TOKEN"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if _, _, err := runVault(t, deps, "new\n", "rotate", "TOKEN"); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	stdout, _, err := runVault(t, deps, "", "get", "TOKEN")
	if err != nil || stdout != "new" {
		t.Fatalf("get after rotate = %q, %v", stdout, err)
	}
	// Rotating a name that is not stored is a not-found refusal, never a
	// silent create.
	if _, _, err := runVault(t, deps, "v\n", "rotate", "ABSENT"); !isCLIKind(err, cascade.KindNotFound) {
		t.Fatalf("rotating an absent name = %v", err)
	}
	names, _, err := runVault(t, deps, "", "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if strings.Contains(names, "ABSENT") {
		t.Fatal("a refused rotate created the secret")
	}
}

func TestVaultCLIGetAbsent(t *testing.T) {
	deps := testVaultDeps(t, okGate{}, nil)
	if _, _, err := runVault(t, deps, "", "get", "ABSENT"); !isCLIKind(err, cascade.KindNotFound) {
		t.Fatalf("get of an absent name = %v", err)
	}
}

// TestElevationGateFailsClosed drives the production gate directly. Each
// case is a state in which local presence cannot be proved, and every one
// must refuse.
func TestElevationGateFailsClosed(t *testing.T) {
	clock := runtime.NewSystemClock()
	cases := map[string]*elevationGate{
		"no keystore and no trust": newElevationGate(nil, nil, clock, func(string) string { return "" }),
		"keystore returns nil": newElevationGate(
			func() elevation.ElevationKeystore { return nil }, nil, clock, func(string) string { return "" }),
		"no input allowed": newElevationGate(
			func() elevation.ElevationKeystore { return availableKeystore{} },
			func() elevation.Backend { return enrolledBackend{} },
			clock,
			func(k string) string {
				if k == "CASCADE_NO_INPUT" {
					return "1"
				}
				return ""
			}),
		"enrolled but no authenticator": newElevationGate(
			func() elevation.ElevationKeystore { return unavailableKeystore{} },
			func() elevation.Backend { return enrolledBackend{} },
			clock, func(string) string { return "" }),
		"authenticator but not enrolled": newElevationGate(
			func() elevation.ElevationKeystore { return availableKeystore{} },
			func() elevation.Backend { return emptyBackend{} },
			clock, func(string) string { return "" }),
	}
	for label, gate := range cases {
		for _, verb := range []string{"vault.get", "vault.rotate"} {
			if err := gate.Authorize(t.Context(), verb); err == nil {
				t.Fatalf("%s: %s was authorised with no proof of local presence", label, verb)
			}
		}
	}
}

// TestElevationGateAllowsWhenBothPreconditionsHold is the negative control:
// the gate must not refuse everything unconditionally, or the tests above
// would pass against a gate that is simply broken.
func TestElevationGateAllowsWhenBothPreconditionsHold(t *testing.T) {
	gate := newElevationGate(
		func() elevation.ElevationKeystore { return availableKeystore{} },
		func() elevation.Backend { return enrolledBackend{} },
		runtime.NewSystemClock(),
		func(string) string { return "" },
	)
	if err := gate.Authorize(t.Context(), "vault.get"); err != nil {
		t.Fatalf("a fully-enrolled host was refused: %v", err)
	}
	// A verb that is not elevated at all passes through.
	if err := gate.Authorize(t.Context(), "vault.list"); err != nil {
		t.Fatalf("a non-elevated verb was refused: %v", err)
	}
}

type availableKeystore struct{}

func (availableKeystore) GenerateKey() error          { return nil }
func (availableKeystore) PubKeyB64() (string, error)  { return "", nil }
func (availableKeystore) Sign([]byte) ([]byte, error) { return nil, nil }
func (availableKeystore) IsAvailable() bool           { return true }
func (availableKeystore) Tier() elevation.StorageTier { return elevation.TierOSKeychain }

type unavailableKeystore struct{ availableKeystore }

func (unavailableKeystore) IsAvailable() bool { return false }

// enrolledBackend reports a trust record carrying a real Ed25519 public
// key, the shape a genuine enrollment leaves behind.
type enrolledBackend struct{}

func (enrolledBackend) Load() (elevation.TrustRecord, bool, error) {
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		return elevation.TrustRecord{}, false, err
	}
	return elevation.TrustRecord{PubKeyB64: base64.StdEncoding.EncodeToString(pub)}, true, nil
}
func (enrolledBackend) Save(elevation.TrustRecord) error { return nil }

type emptyBackend struct{}

func (emptyBackend) Load() (elevation.TrustRecord, bool, error) {
	return elevation.TrustRecord{}, false, nil
}
func (emptyBackend) Save(elevation.TrustRecord) error { return nil }
