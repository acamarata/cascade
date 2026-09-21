package plugins

// Purpose (this file): unit coverage for branches
//   cascadepa_install_installer_test.go / _installer_elevation_test.go
//   never reach: addInstalled's real-metadata-record path (every existing
//   CandidateSourceInstalled test uses a FRESH store, so LoadMetadata
//   always answers ok=false there), addRegistry's own init-failure branch
//   (a distinct source line from addInstalled's identical check),
//   addRegistry's real AddPlugin-failure branch (a verified-but-unparsable
//   artifact), and cascadepa_install_verify.go's D5 non-https REGISTRY
//   BASE url refusal (distinct from the existing non-https DownloadURL
//   test). Round-2 rework, T0 decisions D3 (coverage) and D5 (https base
//   url).
// SPORT: internal/plugins:cascadepa-install-wiring (TEST) -- FIX P1-E24-W5-S50-T4 (D3, D5).

import (
	"context"
	"crypto/ed25519"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
	"github.com/acamarata/cascade/plugins/cascade-pa/install"
)

// TestInstallerAdapter_CandidateSourceInstalled_RealMetadataRecordFound
// drives addInstalled's ok==true branch: a plugin genuinely installed via
// the registry path first, so a real metadata record exists, and a
// SECOND, installed-first candidate for the SAME plugin id must report it
// from that real record -- never fall through to the RuntimeBuiltin
// no-record trust case every OTHER CandidateSourceInstalled test exercises.
func TestInstallerAdapter_CandidateSourceInstalled_RealMetadataRecordFound(t *testing.T) {
	raw, checksum, signature, pub := signedFixture(t, noRequiresManifest)
	fetcher := &fakeRegistryFetcher{artifact: raw}
	a := newTestInstallerAdapter(t, "https://registry.example/", fetcher, pub)

	regReq := install.AddRequest{Candidate: plugin.Candidate{
		PluginID: "no-requires-plugin", Source: plugin.CandidateSourceRegistry,
		RegistryEntry: plugin.RegistryIndexEntry{ID: "no-requires-plugin", LatestVersion: "1.0.0",
			Versions: []plugin.RegistryVersionEntry{{Version: "1.0.0", Checksum: checksum, Signature: signature}}},
	}}
	if _, err := a.Add(context.Background(), regReq); err != nil {
		t.Fatalf("registry Add: %v", err)
	}

	installedReq := install.AddRequest{Candidate: plugin.Candidate{
		PluginID: "no-requires-plugin", Source: plugin.CandidateSourceInstalled,
		Manifest: plugin.Manifest{ID: "no-requires-plugin", Version: "1.0.0", Runtime: plugin.RuntimeWasm},
	}}
	res, err := a.Add(context.Background(), installedReq)
	if err != nil {
		t.Fatalf("installed Add: %v", err)
	}
	if res.Outcome != install.AddOutcomeAlreadyInstalled {
		t.Fatalf("Outcome = %v, want AddOutcomeAlreadyInstalled from the real metadata record LoadMetadata found", res.Outcome)
	}
}

// TestInstallerAdapter_CandidateSourceRegistry_InitFailurePropagates drives
// addRegistry's OWN init-failure check (a distinct source line from
// addInstalled's identical guard, which TestInstallerAdapter_Init_PathResolutionFailureRefuses
// already covers only for the installed-first branch).
func TestInstallerAdapter_CandidateSourceRegistry_InitFailurePropagates(t *testing.T) {
	wantErr := errors.New("boom: no home directory")
	resolvePaths := func() (runtime.PathProvider, error) { return nil, wantErr }
	a := newInstallerAdapter(resolvePaths, testkit.NewFrozenClock(fixedInstallTestTime), newSharedCascadeStore())
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

// TestInstallerAdapter_CandidateSourceRegistry_VerifiedButUnparsableArtifactRefuses
// drives addRegistry's non-elevated AddPlugin-failure branch: the fixture
// bytes are NOT a valid manifest, but the checksum/signature still verify
// (verifiedArtifactBytes only checks integrity, never manifest shape), so
// AddPlugin's own real ParseManifest call is what fails here, not this
// file's own verification.
//
// REWORK (round-3, T0 decision D3, FLAG 6): round-2 asserted only
// err != nil, which an unrelated failure (a broken store, say) would also
// satisfy. The message must name the real parse failure -- ParseManifest
// (pkg/plugin/loader.go) always prefixes its own errors with "plugin
// manifest:", so asserting that substring proves THIS specific refusal
// fired, not merely SOME error.
func TestInstallerAdapter_CandidateSourceRegistry_VerifiedButUnparsableArtifactRefuses(t *testing.T) {
	raw, checksum, signature, pub := signedFixture(t, "this is not a valid cascade plugin manifest")
	fetcher := &fakeRegistryFetcher{artifact: raw}
	a := newTestInstallerAdapter(t, "https://registry.example/", fetcher, pub)
	req := install.AddRequest{Candidate: plugin.Candidate{
		PluginID: "garbage-plugin", Source: plugin.CandidateSourceRegistry,
		RegistryEntry: plugin.RegistryIndexEntry{ID: "garbage-plugin", LatestVersion: "1.0.0",
			Versions: []plugin.RegistryVersionEntry{{Version: "1.0.0", Checksum: checksum, Signature: signature}}},
	}}
	_, err := a.Add(context.Background(), req)
	if err == nil {
		t.Fatal("Add: err = nil, want the real AddPlugin/ParseManifest refusal for an unparsable (but verified) artifact")
	}
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("Add: err = %v, want KindInvalidInput (ParseManifest's own decode-failure Kind)", err)
	}
	if !strings.Contains(err.Error(), "plugin manifest") {
		t.Fatalf("Add: err = %v, want it to name the manifest parse failure (ParseManifest's own \"plugin manifest:\" prefix)", err)
	}
}

// TestInstallerAdapter_CandidateSourceRegistry_ElevatedUnparsableArtifactRefuses
// drives addRegistry's ELEVATED-branch ParseManifest-failure line
// directly: the request already carries a (test-minted, real) Witness on
// its FIRST call, so the non-elevated AddPlugin branch never runs at all
// -- verifiedArtifactBytes still verifies (checksum/signature only, never
// manifest shape) and caches the artifact, and the elevated branch's own
// plugin.ParseManifest call is what fails on these unparsable-but-verified
// bytes.
// REWORK (round-3, T0 decision D3, FLAG 6): same fix as its non-elevated
// sibling above -- assert the message names the real manifest parse
// failure, not merely that Add returned SOME error.
func TestInstallerAdapter_CandidateSourceRegistry_ElevatedUnparsableArtifactRefuses(t *testing.T) {
	raw, checksum, signature, pub := signedFixture(t, "this is not a valid cascade plugin manifest")
	fetcher := &fakeRegistryFetcher{artifact: raw}
	a := newTestInstallerAdapter(t, "https://registry.example/", fetcher, pub)
	req := install.AddRequest{
		Candidate: plugin.Candidate{PluginID: "garbage-elevated-plugin", Source: plugin.CandidateSourceRegistry,
			RegistryEntry: plugin.RegistryIndexEntry{ID: "garbage-elevated-plugin", LatestVersion: "1.0.0",
				Versions: []plugin.RegistryVersionEntry{{Version: "1.0.0", Checksum: checksum, Signature: signature}}}},
		Witness: testWitness(t, "test-attestation-elevated-garbage"),
	}
	_, err := a.Add(context.Background(), req)
	if err == nil {
		t.Fatal("Add: err = nil, want the real ParseManifest refusal on the elevated branch for an unparsable (but verified) artifact")
	}
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("Add: err = %v, want KindInvalidInput (ParseManifest's own decode-failure Kind)", err)
	}
	if !strings.Contains(err.Error(), "plugin manifest") {
		t.Fatalf("Add: err = %v, want it to name the manifest parse failure (ParseManifest's own \"plugin manifest:\" prefix)", err)
	}
}

// TestInstallerAdapter_CandidateSourceRegistry_NonHTTPSRegistryBaseURLRefuses
// is the T0 decision D5 proof: entry.DownloadURL is empty (so the
// DownloadURL guard never fires), and the [registry] base url itself is
// plaintext -- refused before any fetch, distinct from the existing
// non-https DownloadURL test.
func TestInstallerAdapter_CandidateSourceRegistry_NonHTTPSRegistryBaseURLRefuses(t *testing.T) {
	fetcher := &fakeRegistryFetcher{artifact: []byte(noRequiresManifest)}
	pub := ed25519.PublicKey(make([]byte, ed25519.PublicKeySize))
	a := newTestInstallerAdapter(t, "http://registry.example/", fetcher, pub)
	req := install.AddRequest{Candidate: plugin.Candidate{
		PluginID: "reg-plugin", Source: plugin.CandidateSourceRegistry,
		RegistryEntry: plugin.RegistryIndexEntry{ID: "reg-plugin", LatestVersion: "1.0.0",
			Versions: []plugin.RegistryVersionEntry{{Version: "1.0.0", Checksum: "deadbeef", Signature: "x"}}},
	}}
	_, err := a.Add(context.Background(), req)
	if err == nil {
		t.Fatal("Add: err = nil, want a refusal for a non-https registry base url")
	}
	if fetcher.calls != 0 {
		t.Fatalf("FetchArtifact called %d times, want 0 -- the non-https base url must refuse before any fetch", fetcher.calls)
	}
}
