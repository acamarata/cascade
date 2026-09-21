package plugins

// Purpose (this file): unit coverage for cascadepa_install_installer.go's
//   and cascadepa_install_verify.go's real Installer adapter -- every
//   branch driven against a REAL, TempDir-backed provider.Store (Art.7.1:
//   never the operator's real home), a fake plugin.RegistryFetcher
//   standing in for the network the no-network-unit-lane gate forbids
//   _test.go files from reaching directly, and REAL Ed25519-signed
//   fixtures (checksum + signature), matching the S-50.T1 registry
//   client's own fixture-signing technique (a throwaway keypair generated
//   in-test, never a hand-crafted signature).
// SPORT: internal/plugins:cascadepa-install-wiring (TEST) -- P1-E24-W5-S50-T4.

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/plugin"
	"github.com/acamarata/cascade/plugins/cascade-pa/install"
)

// fixedInstallTestTime is the frozen instant every test clock in this file
// uses (Art.7.3: no sleeps, no bare time.Now).
var fixedInstallTestTime = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

const noRequiresManifest = `
id = "no-requires-plugin"
name = "No Requires Plugin"
schema = "cascade.plugin/v2"
version = "1.0.0"
host_version = ">=2.0.0"
runtime = "wasm"
`

const processTierManifest = `
id = "process-plugin"
name = "Process Plugin"
schema = "cascade.plugin/v2"
version = "1.0.0"
host_version = ">=2.0.0"
runtime = "process"
`

const grantExpandingManifest = `
id = "grant-expand-plugin"
name = "Grant Expand Plugin"
schema = "cascade.plugin/v2"
version = "1.0.0"
host_version = ">=2.0.0"
runtime = "wasm"
requires = ["storage.domain"]
`

// tamperedGrantExpandingManifest names a DIFFERENT version, so a test that
// swaps this in for the second fetch call proves the installer never
// installs it (TOCTOU closed by verifiedArtifactBytes' cache).
const tamperedGrantExpandingManifest = `
id = "grant-expand-plugin"
name = "Grant Expand Plugin (tampered)"
schema = "cascade.plugin/v2"
version = "9.9.9"
host_version = ">=2.0.0"
runtime = "wasm"
requires = ["storage.domain", "net.egress"]
`

// signedFixture returns bytes signed with a fresh, in-test Ed25519 keypair
// -- checksum and signature both real (S-50.T1's own fixture-signing
// technique: never a hand-crafted signature), plus the public key to
// verify against.
func signedFixture(t *testing.T, manifest string) (bytes []byte, checksum, signature string, pub ed25519.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate fixture signing key: %v", err)
	}
	bytes = []byte(manifest)
	sum := sha256.Sum256(bytes)
	checksum = hex.EncodeToString(sum[:])
	signature = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, bytes))
	return bytes, checksum, signature, pub
}

// fakeRegistryFetcher stands in for a real HTTPS GET, matching
// registryfetch's own Doer-seam design intent. calls counts FetchArtifact
// invocations (TOCTOU proof); sequence overrides artifact per call index
// when non-empty.
type fakeRegistryFetcher struct {
	artifact []byte
	err      error
	sequence [][]byte
	calls    int
}

func (f *fakeRegistryFetcher) FetchIndex(context.Context) ([]byte, error) { return nil, nil }
func (f *fakeRegistryFetcher) FetchArtifact(context.Context, plugin.RegistryVersionEntry) ([]byte, error) {
	i := f.calls
	f.calls++
	if i < len(f.sequence) {
		return f.sequence[i], f.err
	}
	return f.artifact, f.err
}

// newTestInstallerAdapter builds a real installerAdapter over a host-
// injected Store (round-3, T0 decision D1: sharedCascadeStore's own
// private-Open fallback is deleted). hostStoreFixture opens the store at
// the SAME dir resolvePaths uses, so the injected Store and the raw
// *sql.DB this adapter opens itself stay on the identical cascade.db file.
func newTestInstallerAdapter(t *testing.T, registryURL string, fetcher plugin.RegistryFetcher, pub ed25519.PublicKey) *installerAdapter {
	t.Helper()
	dir := t.TempDir()
	// fakePathProvider.DataDir (cascadepa_wiring_test.go's existing
	// fixture) always answers "/fake/data"; this installer needs a REAL,
	// disposable directory, so tempDataPathProvider points DataDir at
	// this test's own t.TempDir() instead.
	resolvePaths := func() (runtime.PathProvider, error) { return tempDataPathProvider{dir: dir}, nil }
	hostStoreFixture(t, dir)
	a := newInstallerAdapter(resolvePaths, testkit.NewFrozenClock(fixedInstallTestTime), newSharedCascadeStore())
	t.Cleanup(func() { _ = a.Close() })
	a.fetcher = func(string) plugin.RegistryFetcher { return fetcher }
	if registryURL != "" {
		a.registryURL = func() (string, error) { return registryURL, nil }
	}
	if pub != nil {
		a.registryPubKey = func() (ed25519.PublicKey, error) { return pub, nil }
	}
	return a
}

// tempDataPathProvider answers DataDir with a real TempDir so
// sqlitestore.Open opens a real, disposable cascade.db per test.
type tempDataPathProvider struct{ dir string }

func (p tempDataPathProvider) Root() string       { return p.dir }
func (p tempDataPathProvider) ConfigPath() string { return filepath.Join(p.dir, "config.toml") }
func (p tempDataPathProvider) SocketPath() string { return filepath.Join(p.dir, "daemon.sock") }
func (p tempDataPathProvider) DataDir() string    { return p.dir }
func (p tempDataPathProvider) LogDir() string     { return filepath.Join(p.dir, "logs") }
func (p tempDataPathProvider) StorageRoot(runtime.Profile) string {
	return filepath.Join(p.dir, "storage")
}

func TestInstallerAdapter_CandidateSourceInstalled_ReportsAlreadyInstalled(t *testing.T) {
	a := newTestInstallerAdapter(t, "", nil, nil)
	req := install.AddRequest{Candidate: plugin.Candidate{
		PluginID: "already-here", Source: plugin.CandidateSourceInstalled,
		Manifest: plugin.Manifest{ID: "already-here", Version: "1.0.0", Runtime: plugin.RuntimeBuiltin},
	}}
	res, err := a.Add(context.Background(), req)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if res.Outcome != install.AddOutcomeAlreadyInstalled {
		t.Fatalf("Outcome = %v, want AddOutcomeAlreadyInstalled -- an installed-first candidate is, by the resolver's own contract, already enabled", res.Outcome)
	}
}

// TestInstallerAdapter_CandidateSourceInstalled_NonBuiltinNoRecordRefuses
// proves the round-1 CR F1c fix: a non-builtin runtime with no host
// metadata record is refused, never reported AlreadyInstalled from
// nothing.
func TestInstallerAdapter_CandidateSourceInstalled_NonBuiltinNoRecordRefuses(t *testing.T) {
	a := newTestInstallerAdapter(t, "", nil, nil)
	req := install.AddRequest{Candidate: plugin.Candidate{
		PluginID: "phantom-wasm", Source: plugin.CandidateSourceInstalled,
		Manifest: plugin.Manifest{ID: "phantom-wasm", Version: "1.0.0", Runtime: plugin.RuntimeWasm},
	}}
	_, err := a.Add(context.Background(), req)
	if err == nil {
		t.Fatal("Add: err = nil, want a refusal for a non-builtin candidate with no host metadata record")
	}
}

func TestInstallerAdapter_CandidateSourceRegistry_UnconfiguredRegistryRefuses(t *testing.T) {
	a := newTestInstallerAdapter(t, "", nil, nil)
	req := install.AddRequest{Candidate: plugin.Candidate{
		PluginID: "reg-plugin", Source: plugin.CandidateSourceRegistry,
		RegistryEntry: plugin.RegistryIndexEntry{ID: "reg-plugin", LatestVersion: "1.0.0",
			Versions: []plugin.RegistryVersionEntry{{Version: "1.0.0", Checksum: "deadbeef", Signature: "x"}}},
	}}
	_, err := a.Add(context.Background(), req)
	if err == nil {
		t.Fatal("Add: err = nil, want a refusal naming the unconfigured registry")
	}
}

func TestInstallerAdapter_CandidateSourceRegistry_UnconfiguredPubKeyRefuses(t *testing.T) {
	a := newTestInstallerAdapter(t, "https://registry.example/", &fakeRegistryFetcher{}, nil)
	req := install.AddRequest{Candidate: plugin.Candidate{
		PluginID: "reg-plugin", Source: plugin.CandidateSourceRegistry,
		RegistryEntry: plugin.RegistryIndexEntry{ID: "reg-plugin", LatestVersion: "1.0.0",
			Versions: []plugin.RegistryVersionEntry{{Version: "1.0.0", Checksum: "deadbeef", Signature: "x"}}},
	}}
	_, err := a.Add(context.Background(), req)
	if err == nil {
		t.Fatal("Add: err = nil, want a refusal naming the unconfigured registry public key")
	}
}

// TestInstallerAdapter_CandidateSourceRegistry_EmptyChecksumRefuses is the
// round-1 CR B1 second proof: an entry whose Checksum is "" installs with
// zero integrity checking under the old code. It must now refuse before
// ever fetching.
func TestInstallerAdapter_CandidateSourceRegistry_EmptyChecksumRefuses(t *testing.T) {
	fetcher := &fakeRegistryFetcher{artifact: []byte(noRequiresManifest)}
	a := newTestInstallerAdapter(t, "https://registry.example/", fetcher, ed25519.PublicKey(make([]byte, ed25519.PublicKeySize)))
	req := install.AddRequest{Candidate: plugin.Candidate{
		PluginID: "reg-plugin", Source: plugin.CandidateSourceRegistry,
		RegistryEntry: plugin.RegistryIndexEntry{ID: "reg-plugin", LatestVersion: "1.0.0",
			Versions: []plugin.RegistryVersionEntry{{Version: "1.0.0", Checksum: "", Signature: "anything"}}},
	}}
	_, err := a.Add(context.Background(), req)
	if err == nil {
		t.Fatal("Add: err = nil, want a refusal for an entry with an empty checksum")
	}
	if fetcher.calls != 0 {
		t.Fatalf("FetchArtifact called %d times, want 0 -- the empty checksum must refuse before any fetch", fetcher.calls)
	}
}

// TestInstallerAdapter_CandidateSourceRegistry_EmptySignatureRefuses is the
// round-1 CR B1 fix item 2 second half: entry.Signature is verified
// nowhere in the old path. It must now refuse before ever fetching.
func TestInstallerAdapter_CandidateSourceRegistry_EmptySignatureRefuses(t *testing.T) {
	fetcher := &fakeRegistryFetcher{artifact: []byte(noRequiresManifest)}
	a := newTestInstallerAdapter(t, "https://registry.example/", fetcher, ed25519.PublicKey(make([]byte, ed25519.PublicKeySize)))
	req := install.AddRequest{Candidate: plugin.Candidate{
		PluginID: "reg-plugin", Source: plugin.CandidateSourceRegistry,
		RegistryEntry: plugin.RegistryIndexEntry{ID: "reg-plugin", LatestVersion: "1.0.0",
			Versions: []plugin.RegistryVersionEntry{{Version: "1.0.0", Checksum: "deadbeef", Signature: ""}}},
	}}
	_, err := a.Add(context.Background(), req)
	if err == nil {
		t.Fatal("Add: err = nil, want a refusal for an entry with an empty signature")
	}
	if fetcher.calls != 0 {
		t.Fatalf("FetchArtifact called %d times, want 0 -- the empty signature must refuse before any fetch", fetcher.calls)
	}
}

// TestInstallerAdapter_CandidateSourceRegistry_TamperedSignatureRefuses
// proves the real Ed25519 signature check actually runs: bytes signed by a
// DIFFERENT key than registryPubKey must refuse.
func TestInstallerAdapter_CandidateSourceRegistry_TamperedSignatureRefuses(t *testing.T) {
	raw, checksum, signature, _ := signedFixture(t, noRequiresManifest)
	_, _, _, wrongPub := signedFixture(t, noRequiresManifest) // a different keypair
	fetcher := &fakeRegistryFetcher{artifact: raw}
	a := newTestInstallerAdapter(t, "https://registry.example/", fetcher, wrongPub)
	req := install.AddRequest{Candidate: plugin.Candidate{
		PluginID: "no-requires-plugin", Source: plugin.CandidateSourceRegistry,
		RegistryEntry: plugin.RegistryIndexEntry{ID: "no-requires-plugin", LatestVersion: "1.0.0",
			Versions: []plugin.RegistryVersionEntry{{Version: "1.0.0", Checksum: checksum, Signature: signature}}},
	}}
	if _, err := a.Add(context.Background(), req); err == nil {
		t.Fatal("Add: err = nil, want a refusal when the signature verifies against the wrong key")
	}
}

func TestInstallerAdapter_CandidateSourceRegistry_FreshInstall(t *testing.T) {
	raw, checksum, signature, pub := signedFixture(t, noRequiresManifest)
	fetcher := &fakeRegistryFetcher{artifact: raw}
	a := newTestInstallerAdapter(t, "https://registry.example/", fetcher, pub)
	req := install.AddRequest{Candidate: plugin.Candidate{
		PluginID: "no-requires-plugin", Source: plugin.CandidateSourceRegistry,
		RegistryEntry: plugin.RegistryIndexEntry{ID: "no-requires-plugin", LatestVersion: "1.0.0",
			Versions: []plugin.RegistryVersionEntry{{Version: "1.0.0", Checksum: checksum, Signature: signature}}},
	}}
	res, err := a.Add(context.Background(), req)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if res.Outcome != install.AddOutcomeInstalled {
		t.Fatalf("Outcome = %v, want AddOutcomeInstalled", res.Outcome)
	}

	// Idempotent re-add: the metadata record AddPlugin wrote is real, so a
	// second Add against the SAME store reports AlreadyInstalled -- proves
	// this adapter reaches the real store, not a discarded write.
	res2, err := a.Add(context.Background(), req)
	if err != nil {
		t.Fatalf("second Add: %v", err)
	}
	if res2.Outcome != install.AddOutcomeAlreadyInstalled {
		t.Fatalf("second Add Outcome = %v, want AddOutcomeAlreadyInstalled (Sec5.9 idempotency)", res2.Outcome)
	}
}
