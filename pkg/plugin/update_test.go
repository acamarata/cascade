// Purpose: tests for CheckUpdate, GrantDiff, ConfirmGrantAcceptance, and
//
//	StageAndVerifyArtifact (update.go), including the end-to-end grant-diff
//	acceptance criteria over the real provenance-stamped fixture at
//	testdata/update/index-bump-with-new-grant.json (see that directory's
//	README.md for generation provenance, Art.2 §2). cmd/cascade/
//	plugin_update_test.go carries the CLI-level TestPluginUpdateCommand
//	(--yes/--accept-grant/CASCADE_NO_INPUT wiring and the actual store
//	commit) — this file proves the registry+verify+grant-diff decision
//	chain those flags drive, without any cobra/env/store machinery pkg/
//	plugin cannot depend on (Art.10.2: pkg/ never imports internal/).
//
// SPORT: pkg/plugin registry-client-update tests (ADD) — P1-E24-W5-S50-T8.
package plugin_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
)

// grantDiffFixturePublicKeyB64 verifies testdata/update/index-bump-with-new-grant.json
// (see that directory's README.md for the matching private key's provenance).
const grantDiffFixturePublicKeyB64 = "C7w0aldmfDgBIL2cf9flHSxf3+o3zS9b9AWyxr9vLXg="

// grantDiffCandidateManifestTOML is byte-for-byte what the throwaway
// fixture generator hashed to produce the "1.1.0" entry's checksum in
// index-bump-with-new-grant.json (README.md). TestGrantDiffFixtureChecksum
// below re-derives that checksum from this exact constant, so a future
// accidental edit of either the constant or the fixture is caught by a
// test rather than trusted by inspection.
const grantDiffCandidateManifestTOML = `schema = "cascade.plugin/v2"
id = "grant-diff-demo"
name = "Grant Diff Demo"
version = "1.1.0"
host_version = ">=1.0.0"
runtime = "builtin"
requires = ["storage.local", "network.egress"]
`

func grantDiffFixtureBytes(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "update", "index-bump-with-new-grant.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return data
}

func grantDiffVerifier(t *testing.T) plugin.Ed25519Verifier {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(grantDiffFixturePublicKeyB64)
	if err != nil {
		t.Fatalf("decode fixture public key: %v", err)
	}
	return plugin.Ed25519Verifier{PublicKey: ed25519.PublicKey(raw)}
}

// updateFakeFetcher is a plugin.RegistryFetcher double keyed by
// DownloadURL for FetchArtifact, distinct from registry_client_test.go's
// fakeFetcher (whose FetchArtifact is deliberately unimplemented — "not
// used by these tests" — since no test in that file downloads an
// artifact).
type updateFakeFetcher struct {
	index      []byte
	indexErr   error
	artifacts  map[string][]byte
	artifactOK bool // when false, FetchArtifact always errors (fetch-failure case)
}

func (f updateFakeFetcher) FetchIndex(context.Context) ([]byte, error) {
	if f.indexErr != nil {
		return nil, f.indexErr
	}
	return f.index, nil
}

func (f updateFakeFetcher) FetchArtifact(_ context.Context, entry plugin.RegistryVersionEntry) ([]byte, error) {
	if !f.artifactOK {
		return nil, cascade.New(cascade.KindUnavailable, "fixture: artifact fetch refused for this test")
	}
	data, ok := f.artifacts[entry.DownloadURL]
	if !ok {
		return nil, cascade.Newf(cascade.KindNotFound, "fixture: no artifact staged for %q", entry.DownloadURL)
	}
	return data, nil
}

func grantDiffClient(t *testing.T, fetcher plugin.RegistryFetcher) *plugin.RegistryClient {
	t.Helper()
	return plugin.NewRegistryClient(plugin.RegistryConfig{}, fetcher, grantDiffVerifier(t), nil, &fakeClock{now: time.Unix(2000, 0)})
}

// TestGrantDiffFixtureChecksum re-derives the fixture's "1.1.0" checksum
// from grantDiffCandidateManifestTOML, so this file's own transcription of
// the manifest constant can never silently drift from what the committed
// fixture actually signs over.
func TestGrantDiffFixtureChecksum(t *testing.T) {
	client := grantDiffClient(t, updateFakeFetcher{index: grantDiffFixtureBytes(t), artifactOK: true})
	entry, err := client.CheckUpdate(context.Background(), "grant-diff-demo", "1.0.0")
	if err != nil {
		t.Fatalf("CheckUpdate: %v", err)
	}
	if entry == nil {
		t.Fatal("CheckUpdate = nil, want the 1.1.0 entry")
	}
	sum := sha256.Sum256([]byte(grantDiffCandidateManifestTOML))
	if got, want := hex.EncodeToString(sum[:]), entry.Checksum; got != want {
		t.Fatalf("sha256(grantDiffCandidateManifestTOML) = %s, want fixture checksum %s (constant drifted from the fixture)", got, want)
	}
}

func TestCheckUpdate_NewerVersionPresent(t *testing.T) {
	client := grantDiffClient(t, updateFakeFetcher{index: grantDiffFixtureBytes(t), artifactOK: true})
	entry, err := client.CheckUpdate(context.Background(), "grant-diff-demo", "1.0.0")
	if err != nil {
		t.Fatalf("CheckUpdate(1.0.0): %v", err)
	}
	if entry == nil || entry.Version != "1.1.0" {
		t.Fatalf("CheckUpdate(1.0.0) = %+v, want *Entry{Version: 1.1.0}", entry)
	}
}

func TestCheckUpdate_AlreadyAtLatest(t *testing.T) {
	client := grantDiffClient(t, updateFakeFetcher{index: grantDiffFixtureBytes(t), artifactOK: true})
	entry, err := client.CheckUpdate(context.Background(), "grant-diff-demo", "1.1.0")
	if err != nil {
		t.Fatalf("CheckUpdate(1.1.0): %v", err)
	}
	if entry != nil {
		t.Fatalf("CheckUpdate(1.1.0) = %+v, want nil (already at latest)", entry)
	}
	// Idempotency (§5.9): a second call over the same installed version
	// reports the identical no-op outcome.
	entry2, err := client.CheckUpdate(context.Background(), "grant-diff-demo", "1.1.0")
	if err != nil || entry2 != nil {
		t.Fatalf("second CheckUpdate(1.1.0) = (%+v, %v), want (nil, nil)", entry2, err)
	}
}

func TestCheckUpdate_PluginNotInRegistry(t *testing.T) {
	client := grantDiffClient(t, updateFakeFetcher{index: grantDiffFixtureBytes(t), artifactOK: true})
	entry, err := client.CheckUpdate(context.Background(), "does-not-exist-in-index", "1.0.0")
	if entry != nil {
		t.Fatalf("CheckUpdate(unknown) entry = %+v, want nil", entry)
	}
	if !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("CheckUpdate(unknown) err = %v, want KindNotFound", err)
	}
	if !bytes.Contains([]byte(err.Error()), []byte("does-not-exist-in-index")) {
		t.Fatalf("CheckUpdate(unknown) err = %v, want it to name the plugin (not just a bare KindNotFound)", err)
	}
}

func TestCheckUpdate_FetchFailurePropagates(t *testing.T) {
	fetchErr := cascade.New(cascade.KindUnavailable, "registry: http fetch failed")
	client := grantDiffClient(t, updateFakeFetcher{indexErr: fetchErr, artifactOK: true})

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("CheckUpdate panicked on fetch failure: %v", r)
		}
	}()
	entry, err := client.CheckUpdate(context.Background(), "grant-diff-demo", "1.0.0")
	if entry != nil {
		t.Fatalf("CheckUpdate on fetch failure = %+v, want nil", entry)
	}
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("CheckUpdate on fetch failure err = %v, want KindUnavailable (propagated verbatim)", err)
	}
}

func TestCheckUpdate_RejectsMalformedInstalledVersion(t *testing.T) {
	client := grantDiffClient(t, updateFakeFetcher{index: grantDiffFixtureBytes(t), artifactOK: true})
	_, err := client.CheckUpdate(context.Background(), "grant-diff-demo", "not-a-semver")
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("CheckUpdate(bad installed version) err = %v, want KindInvalidInput", err)
	}
}

func assertStagingEmpty(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read staging dir %s: %v", dir, err)
	}
	if len(entries) != 0 {
		t.Fatalf("staging dir %s contains %d entries after StageAndVerifyArtifact returned, want 0 (staged file must always be cleaned up)", dir, len(entries))
	}
}

func TestGrantDiff_NoChange(t *testing.T) {
	added, removed := plugin.GrantDiff([]string{"a", "b"}, []string{"b", "a"})
	if len(added) != 0 || len(removed) != 0 {
		t.Fatalf("GrantDiff(same set, reordered) = added=%v removed=%v, want both empty", added, removed)
	}
}

func TestGrantDiff_AddedAndRemoved(t *testing.T) {
	added, removed := plugin.GrantDiff([]string{"a", "b"}, []string{"b", "c"})
	if len(added) != 1 || added[0] != "c" {
		t.Fatalf("GrantDiff added = %v, want [c]", added)
	}
	if len(removed) != 1 || removed[0] != "a" {
		t.Fatalf("GrantDiff removed = %v, want [a]", removed)
	}
}

func TestConfirmGrantAcceptance_EmptyDiffNeedsNoConfirmation(t *testing.T) {
	if err := plugin.ConfirmGrantAcceptance(nil, nil); err != nil {
		t.Fatalf("ConfirmGrantAcceptance(nil, nil) = %v, want nil (no diff, no confirmation required)", err)
	}
}

func TestStageAndVerifyArtifact_FetchFailureStagesNothing(t *testing.T) {
	client := grantDiffClient(t, updateFakeFetcher{artifactOK: false})
	dir := t.TempDir()
	_, err := client.StageAndVerifyArtifact(context.Background(), plugin.RegistryVersionEntry{Version: "9.9.9", Checksum: "00"}, dir)
	if err == nil {
		t.Fatal("StageAndVerifyArtifact with a refusing fetcher = nil error, want non-nil")
	}
	assertStagingEmpty(t, dir)
}
