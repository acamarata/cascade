package plugins

// Purpose (this file): platform-agnostic unit coverage for
//   cascadepa_install_elevator.go's real attestation flow -- NO REAL
//   KEYCHAIN and NO REAL HOME (Art.7.1, matching
//   cascadepa_bridge_wiring_test.go's own hard rule): every
//   ElevationKeystore here is an in-memory fake, but the surrounding
//   orchestration (rpc.NonceLedger, rpc.ElevationMiddleware,
//   rpc.VerifyAttestation, elevation.ElevationTrustStore over a REAL
//   TempDir-backed elevation.NewFileBackend) is the genuine production
//   code, exercised end to end. Every test in this file refuses (or
//   verifies a pure-function default) WITHOUT ever depending on a real
//   ELEVATION_REQUIRED nonce being issued -- either it refuses before
//   Elevate ever calls the gate (CASCADE_NO_INPUT, Windows-tier-2
//   keystore, unenrolled host), or it only asserts that Elevate refused
//   with Approved:false (auth failure, wrong enrolled key), which holds
//   whether the refusal came from the real local-auth/verify failure
//   (POSIX) or from the Windows tier-2 challenge refusal
//   (issueInstallChallenge, cascadepa_install_elevator.go) -- so every one
//   of these runs, and passes, on the windows/amd64 CI lane too. The ONE
//   test that needs a genuinely APPROVED real ceremony --
//   TestInstallElevator_EnrolledAndSignedApproves -- moved to the
//   `!windows`-tagged cascadepa_install_elevator_posix_test.go (CI fix
//   ci-fix12, mirroring cmd/cascade/backup_elevation_test.go /
//   backup_elevation_realceremony_test.go's identical split,
//   P1-E19-W4-S42-T3). Shared fixtures live in the untagged
//   cascadepa_install_elevator_helpers_test.go so both files see them.
// SPORT: internal/plugins:cascadepa-install-wiring (TEST) -- P1-E24-W5-S50-T4.

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/elevation"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/testkit"
)

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

// TestInstallElevator_AuthFailureRefuses is the mutation-adjacent case: a
// real local-auth decline (Sign returning ErrAuthFailed) must never read
// as Approved:true -- the fail-closed contract this file's header
// promises independent of the daemon's own un-wired ElevationMiddleware
// gap. On Windows the real ceremony never reaches Sign at all
// (issueInstallChallenge refuses first with elevation.ErrWindowsTier2),
// but the assertions below hold either way: an error, Approved:false, and
// an invalid Witness.
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
// never approved on a mismatched signature. On Windows the real ceremony
// never reaches VerifyAttestation (issueInstallChallenge refuses first
// with elevation.ErrWindowsTier2), but the assertions below -- an error
// and Approved:false -- hold either way.
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
