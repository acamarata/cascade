package plugins

// Purpose (this file): the elevation/TOCTOU/witness/error-path half of
//   cascadepa_install_installer_test.go's coverage, split out to keep that
//   file under the 300-line cap. Fixtures (signedFixture,
//   fakeRegistryFetcher, newTestInstallerAdapter, tempDataPathProvider) and
//   the manifest constants stay in the sibling file; this one drives the
//   elevated-retry, TOCTOU-closed, witness-refusal, process-tier-refusal,
//   and error-propagation branches.
// SPORT: internal/plugins:cascadepa-install-wiring (TEST) -- P1-E24-W5-S50-T4.

import (
	"context"
	"crypto/ed25519"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/plugin"
	"github.com/acamarata/cascade/plugins/cascade-pa/install"
)

// passingAttestationVerifier is an install.AttestationVerifier double that
// always succeeds -- these installer-focused tests need SOME valid
// ElevationWitness to drive the elevated-retry branch, but they are
// exercising installerAdapter.Add, not the real attestation flow itself
// (that flow's own genuine verification -- wrong key, auth failure,
// windows tier-2, unenrolled host -- is proven end to end by
// cascadepa_install_elevator_test.go). Going through the real
// install.VerifyElevationWitness constructor (never a literal) still
// proves these fixtures cannot bypass the "unexported fields" contract.
type passingAttestationVerifier struct{}

func (passingAttestationVerifier) Verify(context.Context, install.Attestation) error { return nil }

// testWitness mints a real ElevationWitness through the only exported
// constructor, install.VerifyElevationWitness, over a verifier double that
// approves -- never a literal or a composite literal around the
// unexported requestID field (which would not compile from this package).
func testWitness(t *testing.T, requestID string) install.ElevationWitness {
	t.Helper()
	w, err := install.VerifyElevationWitness(context.Background(), passingAttestationVerifier{}, install.Attestation{RequestID: requestID})
	if err != nil {
		t.Fatalf("VerifyElevationWitness: %v", err)
	}
	return w
}

// TestInstallerAdapter_CandidateSourceRegistry_TOCTOUClosed is the round-1
// CR fix item 3(a) proof: the fetcher returns DIFFERENT bytes on the
// second call (a tampered artifact), and the elevated retry must still
// install the FIRST, already-verified buffer -- never re-fetch, never
// trust the second response.
func TestInstallerAdapter_CandidateSourceRegistry_TOCTOUClosed(t *testing.T) {
	honest, checksum, signature, pub := signedFixture(t, grantExpandingManifest)
	fetcher := &fakeRegistryFetcher{sequence: [][]byte{honest, []byte(tamperedGrantExpandingManifest)}}
	a := newTestInstallerAdapter(t, "https://registry.example/", fetcher, pub)
	req := install.AddRequest{Candidate: plugin.Candidate{
		PluginID: "grant-expand-plugin", Source: plugin.CandidateSourceRegistry,
		RegistryEntry: plugin.RegistryIndexEntry{ID: "grant-expand-plugin", LatestVersion: "1.0.0",
			Versions: []plugin.RegistryVersionEntry{{Version: "1.0.0", Checksum: checksum, Signature: signature}}},
	}}
	res, err := a.Add(context.Background(), req)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if res.Outcome != install.AddOutcomeElevationRequired {
		t.Fatalf("Outcome = %v, want AddOutcomeElevationRequired", res.Outcome)
	}

	req.Witness = testWitness(t, "test-attestation-1")
	res2, err := a.Add(context.Background(), req)
	if err != nil {
		t.Fatalf("elevated Add: %v", err)
	}
	if res2.Outcome != install.AddOutcomeInstalled {
		t.Fatalf("elevated Add Outcome = %v, want AddOutcomeInstalled", res2.Outcome)
	}
	if fetcher.calls != 1 {
		t.Fatalf("FetchArtifact called %d times, want exactly 1 -- the elevated retry must reuse the cached, already-verified buffer", fetcher.calls)
	}
	rec, ok, err := LoadMetadata(context.Background(), a.store, "grant-expand-plugin")
	if err != nil || !ok {
		t.Fatalf("LoadMetadata: ok=%v err=%v", ok, err)
	}
	if rec.InstalledVersion != "1.0.0" {
		t.Fatalf("InstalledVersion = %q, want %q (the FIRST, verified fetch) -- a %q record means the tampered second fetch was installed",
			rec.InstalledVersion, "1.0.0", "9.9.9")
	}
}

func TestInstallerAdapter_CandidateSourceRegistry_GrantExpansionElevatedInstallSucceeds(t *testing.T) {
	raw, checksum, signature, pub := signedFixture(t, grantExpandingManifest)
	fetcher := &fakeRegistryFetcher{artifact: raw}
	a := newTestInstallerAdapter(t, "https://registry.example/", fetcher, pub)
	req := install.AddRequest{Candidate: plugin.Candidate{
		PluginID: "grant-expand-plugin", Source: plugin.CandidateSourceRegistry,
		RegistryEntry: plugin.RegistryIndexEntry{ID: "grant-expand-plugin", LatestVersion: "1.0.0",
			Versions: []plugin.RegistryVersionEntry{{Version: "1.0.0", Checksum: checksum, Signature: signature}}},
	}}
	res, err := a.Add(context.Background(), req)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if res.Outcome != install.AddOutcomeElevationRequired {
		t.Fatalf("Outcome = %v, want AddOutcomeElevationRequired -- a fresh install requesting any capability is a grant expansion over the empty set", res.Outcome)
	}

	// TestInstallerAdapter_ElevatedAddWithoutWitnessRefuses (below) covers
	// the bare-assertion-refused case; here the witness is real.
	req.Witness = testWitness(t, "test-attestation-2")
	res2, err := a.Add(context.Background(), req)
	if err != nil {
		t.Fatalf("elevated Add: %v", err)
	}
	if res2.Outcome != install.AddOutcomeInstalled {
		t.Fatalf("elevated Add Outcome = %v, want AddOutcomeInstalled (real ProvisionElevated wasm-tier path)", res2.Outcome)
	}

	t.Run("AlreadyInstalledOnElevatedBranchRefusesOverwrite", func(t *testing.T) {
		// round-1 CR fix item 8: a second elevated Add for the SAME
		// version must report AlreadyInstalled, never re-provision.
		res3, err := a.Add(context.Background(), req)
		if err != nil {
			t.Fatalf("second elevated Add: %v", err)
		}
		if res3.Outcome != install.AddOutcomeAlreadyInstalled {
			t.Fatalf("second elevated Add Outcome = %v, want AddOutcomeAlreadyInstalled (Sec5.9 on the elevated branch)", res3.Outcome)
		}
	})
}

// TestInstallerAdapter_ElevatedAddWithoutWitnessRefuses is the round-1 CR
// fix item 9 proof: a request whose Candidate needs elevation but carries
// no ElevationWitness (the caller's bare assertion) must still take the
// non-elevated branch and report ElevationRequired again, never install.
func TestInstallerAdapter_ElevatedAddWithoutWitnessRefuses(t *testing.T) {
	raw, checksum, signature, pub := signedFixture(t, grantExpandingManifest)
	fetcher := &fakeRegistryFetcher{artifact: raw}
	a := newTestInstallerAdapter(t, "https://registry.example/", fetcher, pub)
	req := install.AddRequest{Candidate: plugin.Candidate{
		PluginID: "grant-expand-plugin", Source: plugin.CandidateSourceRegistry,
		RegistryEntry: plugin.RegistryIndexEntry{ID: "grant-expand-plugin", LatestVersion: "1.0.0",
			Versions: []plugin.RegistryVersionEntry{{Version: "1.0.0", Checksum: checksum, Signature: signature}}},
	}}
	res, err := a.Add(context.Background(), req)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if res.Outcome != install.AddOutcomeElevationRequired {
		t.Fatalf("Outcome = %v, want AddOutcomeElevationRequired -- a zero Witness must never install", res.Outcome)
	}
}

// TestInstallerAdapter_CandidateSourceRegistry_ProcessTierNeverLaunches proves
// this adapter never papers over dispatch.go's own real, permanent
// process-tier refusal.
func TestInstallerAdapter_CandidateSourceRegistry_ProcessTierNeverLaunches(t *testing.T) {
	raw, checksum, signature, pub := signedFixture(t, processTierManifest)
	fetcher := &fakeRegistryFetcher{artifact: raw}
	a := newTestInstallerAdapter(t, "https://registry.example/", fetcher, pub)
	req := install.AddRequest{Candidate: plugin.Candidate{
		PluginID: "process-plugin", Source: plugin.CandidateSourceRegistry,
		RegistryEntry: plugin.RegistryIndexEntry{ID: "process-plugin", LatestVersion: "1.0.0",
			Versions: []plugin.RegistryVersionEntry{{Version: "1.0.0", Checksum: checksum, Signature: signature}}},
	}}
	res, err := a.Add(context.Background(), req)
	if err != nil {
		t.Fatalf("Add (decide phase): %v", err)
	}
	if res.Outcome != install.AddOutcomeElevationRequired {
		t.Fatalf("Outcome = %v, want AddOutcomeElevationRequired for a process-tier candidate", res.Outcome)
	}

	req.Witness = testWitness(t, "test-attestation-3")
	if _, err := a.Add(context.Background(), req); err == nil {
		t.Fatal("elevated Add: err = nil, want the real process-tier trust-gate refusal, never a fabricated install")
	}
}

func TestInstallerAdapter_CandidateSourceRegistry_FetchFailurePropagates(t *testing.T) {
	wantErr := errors.New("boom: registry unreachable")
	fetcher := &fakeRegistryFetcher{err: wantErr}
	pub := ed25519.PublicKey(make([]byte, ed25519.PublicKeySize))
	a := newTestInstallerAdapter(t, "https://registry.example/", fetcher, pub)
	req := install.AddRequest{Candidate: plugin.Candidate{
		PluginID: "reg-plugin", Source: plugin.CandidateSourceRegistry,
		RegistryEntry: plugin.RegistryIndexEntry{ID: "reg-plugin", LatestVersion: "1.0.0",
			Versions: []plugin.RegistryVersionEntry{{Version: "1.0.0", Checksum: "deadbeef", Signature: "x"}}},
	}}
	_, err := a.Add(context.Background(), req)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Add: err = %v, want it to wrap %v", err, wantErr)
	}
}

// TestInstallerAdapter_CandidateSourceRegistry_NonHTTPSDownloadURLRefuses
// is the round-1 CR fix item 2 (second half) proof: a plaintext
// DownloadURL is refused before any fetch -- a MITM on an http:// fetch
// controls what gets checksummed, which makes the checksum/signature
// check moot.
func TestInstallerAdapter_CandidateSourceRegistry_NonHTTPSDownloadURLRefuses(t *testing.T) {
	fetcher := &fakeRegistryFetcher{artifact: []byte(noRequiresManifest)}
	pub := ed25519.PublicKey(make([]byte, ed25519.PublicKeySize))
	a := newTestInstallerAdapter(t, "https://registry.example/", fetcher, pub)
	req := install.AddRequest{Candidate: plugin.Candidate{
		PluginID: "reg-plugin", Source: plugin.CandidateSourceRegistry,
		RegistryEntry: plugin.RegistryIndexEntry{ID: "reg-plugin", LatestVersion: "1.0.0",
			Versions: []plugin.RegistryVersionEntry{{Version: "1.0.0", Checksum: "deadbeef", Signature: "x",
				DownloadURL: "http://registry.example/artifact.tar"}}},
	}}
	_, err := a.Add(context.Background(), req)
	if err == nil {
		t.Fatal("Add: err = nil, want a refusal for a non-https download url")
	}
	if fetcher.calls != 0 {
		t.Fatalf("FetchArtifact called %d times, want 0 -- the non-https url must refuse before any fetch", fetcher.calls)
	}
}

func TestInstallerAdapter_UnrecognizedSource(t *testing.T) {
	a := newTestInstallerAdapter(t, "", nil, nil)
	req := install.AddRequest{Candidate: plugin.Candidate{PluginID: "x", Source: "bogus"}}
	_, err := a.Add(context.Background(), req)
	if err == nil {
		t.Fatal("Add: err = nil, want a refusal for an unrecognized candidate source")
	}
}

func TestInstallerAdapter_CandidateSourceInstalled_LoadMetadataErrorPropagates(t *testing.T) {
	a := newTestInstallerAdapter(t, "", nil, nil)
	// An empty PluginID fails LoadMetadata's own validateName check with a
	// real KindInvalidInput error before it ever touches the store --
	// exercising the real error-propagation branch, not a fabricated one.
	req := install.AddRequest{Candidate: plugin.Candidate{Source: plugin.CandidateSourceInstalled}}
	_, err := a.Add(context.Background(), req)
	if err == nil {
		t.Fatal("Add: err = nil, want LoadMetadata's real empty-name refusal")
	}
}

func TestInstallerAdapter_CandidateSourceRegistry_NoMatchingVersionRefuses(t *testing.T) {
	a := newTestInstallerAdapter(t, "https://registry.example/", &fakeRegistryFetcher{}, nil)
	req := install.AddRequest{Candidate: plugin.Candidate{
		PluginID: "reg-plugin", Source: plugin.CandidateSourceRegistry,
		RegistryEntry: plugin.RegistryIndexEntry{ID: "reg-plugin", LatestVersion: "2.0.0",
			Versions: []plugin.RegistryVersionEntry{{Version: "1.0.0"}}},
	}}
	_, err := a.Add(context.Background(), req)
	if err == nil {
		t.Fatal("Add: err = nil, want a refusal when latest_version names no matching versions[] entry")
	}
}

func TestInstallerAdapter_Init_PathResolutionFailureRefuses(t *testing.T) {
	wantErr := errors.New("boom: no home directory")
	resolvePaths := func() (runtime.PathProvider, error) { return nil, wantErr }
	a := newInstallerAdapter(resolvePaths, testkit.NewFrozenClock(fixedInstallTestTime), newSharedCascadeStore())
	req := install.AddRequest{Candidate: plugin.Candidate{PluginID: "x", Source: plugin.CandidateSourceInstalled}}
	_, err := a.Add(context.Background(), req)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Add: err = %v, want it to wrap %v", err, wantErr)
	}
}

// TestInstallerAdapter_Init_UnopenableDataDirRefuses drives the real raw
// *sql.DB open failure branch: DataDir resolves to a path whose parent
// segment is a plain FILE, not a directory, so cascade.db can never be
// created under it. The injected host Store lives at an UNRELATED, valid
// TempDir (round-3, T0 decision D1: shared.open() no longer resolves any
// path of its own, so it succeeds independently of resolvePaths) -- this
// proves the failure this test targets is installerAdapter's own raw
// sql.Open, not shared.open()'s now-deleted fallback.
func TestInstallerAdapter_Init_UnopenableDataDirRefuses(t *testing.T) {
	notADir := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(notADir, []byte("x"), 0o600); err != nil {
		t.Fatalf("write fixture file: %v", err)
	}
	resolvePaths := func() (runtime.PathProvider, error) {
		return tempDataPathProvider{dir: filepath.Join(notADir, "data")}, nil
	}
	hostStoreFixture(t, t.TempDir())
	a := newInstallerAdapter(resolvePaths, testkit.NewFrozenClock(fixedInstallTestTime), newSharedCascadeStore())
	t.Cleanup(func() { _ = a.Close() })
	req := install.AddRequest{Candidate: plugin.Candidate{PluginID: "x", Source: plugin.CandidateSourceInstalled}}
	if _, err := a.Add(context.Background(), req); err == nil {
		t.Fatal("Add: err = nil, want the real cascade.db open failure")
	}
}
