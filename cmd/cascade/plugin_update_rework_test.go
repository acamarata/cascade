// Purpose: the S-50.T8 REWORK pass's own new cmd/cascade-level tests
// (adversarial CR verdict, /tmp/cascade-evidence/s50t8-cr-verdict.txt,
// FIX-1/2 and FIX-9) — split into a second file purely to keep
// plugin_update_test.go under Art.10.3's 300-line cap while landing every
// named check, mirroring that file's own header note about the fixture-
// key/fetcher-double conventions it establishes (reused here, not
// reimplemented, except where a test needs a DIFFERENT fetcher double —
// tamperedUpdateFetcher — that the base file does not).
//
// SPORT: cli/plugin-lifecycle/CHANGE tests, rework (P1-E24-W5-S50-T8).
package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
)

// testPluginUpdateYesWithAcceptGrantStillRefusedWithoutDaemon is the
// S-50.T8 rework's own corrected expectation (adversarial CR FIX-1/2,
// T0 decision D1 items 1/2): --accept-grant is the operator's REQUIRED
// acknowledgement of a grant expansion, but it is never the AUTHORITY —
// with no daemon, plugins.UpdatePlugin refuses the same way it would with
// no --accept-grant at all (ErrDaemonRequiredForElevatedPluginOp,
// KindUnavailable), and the store is left exactly as it was. Before this
// rework, this same input committed the update directly via
// plugins.SaveMetadata, bypassing UpdatePlugin's elevation gate entirely
// — that is the exact defect this test now pins shut.
func testPluginUpdateYesWithAcceptGrantStillRefusedWithoutDaemon(t *testing.T) {
	deps := updateTestDeps(t)
	seedPluginUpdateMetadata(t, deps, pluginUpdateBuiltin(plugins.PluginMetadata{Name: "grant-diff-demo", InstalledVersion: "1.0.0", Grants: []string{"storage.local"}}))
	_, stderr, err := runPluginCLI(t, deps, "update", "grant-diff-demo", "--yes", "--accept-grant=network.egress")
	if err == nil {
		t.Fatal("--yes --accept-grant=network.egress with no daemon = nil error, want the daemon-required refusal (acknowledgement is not authority)")
	}
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("err = %v, want KindUnavailable (ErrDaemonRequiredForElevatedPluginOp)", err)
	}
	if !strings.Contains(err.Error(), "daemon") && !strings.Contains(stderr, "daemon") {
		t.Fatalf("err/stderr do not name the daemon requirement: err=%v stderr=%q", err, stderr)
	}
	rec := loadPluginUpdateMetadata(t, deps, "grant-diff-demo")
	if rec.InstalledVersion != "1.0.0" {
		t.Fatalf("stored InstalledVersion = %q after a refused elevated update, want unchanged 1.0.0", rec.InstalledVersion)
	}
	if len(rec.Grants) != 1 {
		t.Fatalf("stored Grants = %v, want the unchanged [storage.local]", rec.Grants)
	}
}

// testPluginUpdateTamperedArtifactRefusesAndLeavesStore is FIX-9's own
// cmd/cascade-level test: bytes that do not match the fixture's checksum
// must refuse with a non-zero exit AND leave the stored version
// untouched — proven here at the actual CLI/store boundary, not only at
// the pkg/plugin StageAndVerifyArtifact unit level (update_e2e_test.go's
// TestPluginUpdateGrantDiffE2e already covers that seam). Note (per the
// rework's own FIX-9 journal requirement): the registry-driven path runs
// no ProcessHandshaker, so this refusal happens purely on the checksum
// mismatch inside plugins.UpdatePlugin, before any handshake concept
// would apply.
func testPluginUpdateTamperedArtifactRefusesAndLeavesStore(t *testing.T) {
	deps := updateTestDeps(t)
	deps.UpdateRegistryClient = func(context.Context) (*plugin.RegistryClient, error) {
		return cliTamperedUpdateClient(t), nil
	}
	seedPluginUpdateMetadata(t, deps, pluginUpdateBuiltin(plugins.PluginMetadata{Name: "grant-diff-demo", InstalledVersion: "1.0.0", Grants: []string{"storage.local", "network.egress"}}))
	_, _, err := runPluginCLI(t, deps, "update", "grant-diff-demo")
	if err == nil {
		t.Fatal("update over tampered artifact bytes = nil error, want non-nil")
	}
	if !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("err = %v, want KindIntegrity", err)
	}
	rec := loadPluginUpdateMetadata(t, deps, "grant-diff-demo")
	if rec.InstalledVersion != "1.0.0" {
		t.Fatalf("stored InstalledVersion = %q after a tampered-artifact refusal, want unchanged 1.0.0", rec.InstalledVersion)
	}
}

// cliTamperedUpdateClient mirrors cliUpdateFakeClient (plugin_update_test.go)
// but serves artifact bytes that do NOT match the fixture's committed
// checksum for 1.1.0 — isolating the tamper-detection path from the real
// (matching) fixture every other test in this package uses.
func cliTamperedUpdateClient(t *testing.T) *plugin.RegistryClient {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "pkg", "plugin", "testdata", "update", "index-bump-with-new-grant.json"))
	if err != nil {
		t.Fatalf("read grant-diff fixture: %v", err)
	}
	pub, err := base64.StdEncoding.DecodeString(grantDiffFixturePublicKeyB64CLI)
	if err != nil {
		t.Fatalf("decode fixture public key: %v", err)
	}
	return plugin.NewRegistryClient(
		plugin.RegistryConfig{}, tamperedUpdateFetcher{index: data},
		plugin.Ed25519Verifier{PublicKey: ed25519.PublicKey(pub)}, nil, runtime.NewSystemClock(),
	)
}

// tamperedUpdateFetcher answers FetchArtifact with bytes that do not match
// the fixture's checksum for any version.
type tamperedUpdateFetcher struct{ index []byte }

func (f tamperedUpdateFetcher) FetchIndex(context.Context) ([]byte, error) { return f.index, nil }

func (f tamperedUpdateFetcher) FetchArtifact(_ context.Context, _ plugin.RegistryVersionEntry) ([]byte, error) {
	return []byte("these are not the real candidate bytes"), nil
}

// testPluginUpdateRemovalOnlyDiffShown is the S-50.T8 confirming review's
// F1: an earlier revision of confirmPluginUpdateGrants (adversarial CR
// FIX-5) returned before rendering anything when added was empty, so a
// diff that only REMOVES a capability (no addition, so no elevation gate
// to pass) was silently never shown even though pluginUpdateGrantDiffView
// already knew how to render one. The seeded record carries an extra
// "extra.cap" grant the 1.1.0 candidate manifest (requires
// [storage.local, network.egress]) does not — a pure removal, so the
// update commits directly (no --yes/--accept-grant needed) and the
// removal-only line must still appear in stdout.
func testPluginUpdateRemovalOnlyDiffShown(t *testing.T) {
	deps := updateTestDeps(t)
	seedPluginUpdateMetadata(t, deps, pluginUpdateBuiltin(plugins.PluginMetadata{
		Name: "grant-diff-demo", InstalledVersion: "1.0.0",
		Grants: []string{"storage.local", "network.egress", "extra.cap"},
	}))
	stdout, stderr, err := runPluginCLI(t, deps, "update", "grant-diff-demo")
	if err != nil {
		t.Fatalf("removal-only update: unexpected error: %v (stderr=%s)", err, stderr)
	}
	if !strings.Contains(stdout, "no longer requires: extra.cap") {
		t.Fatalf("stdout = %q, want it to contain the removal-only grant diff line", stdout)
	}
}

// testPluginUpdateNotInstalled is the S-50.T8 confirming review's F3:
// loadInstalledForUpdate's KindNotFound refusal (adversarial CR FIX-7,
// replacing a dead "empty InstalledVersion implicitly means fresh install"
// branch) was never exercised end-to-end through the CLI — only at the
// pkg/plugin/internal/plugins unit level.
func testPluginUpdateNotInstalled(t *testing.T) {
	deps := updateTestDeps(t)
	_, _, err := runPluginCLI(t, deps, "update", "grant-diff-demo")
	if err == nil {
		t.Fatal("update with no seeded record = nil error, want the not-installed refusal")
	}
	if !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("err = %v, want KindNotFound", err)
	}
	if !strings.Contains(err.Error(), "is not installed") {
		t.Fatalf("err = %v, want it to name the not-installed refusal", err)
	}
}

// passthroughVerifier is a plugin.RegistryVerifier double that accepts any
// bytes unconditionally — TestResolveVerifiedCandidate (F2 below) isolates
// resolveVerifiedCandidate's own id/version checks, not signature
// verification (already covered by pkg/plugin/verify_artifact_test.go and
// this package's TestPluginUpdateTamperedArtifactRefusesAndLeavesStore).
type passthroughVerifier struct{}

func (passthroughVerifier) VerifyIndex(context.Context, []byte) (plugin.RegistryIndex, error) {
	return plugin.RegistryIndex{}, nil
}

func (passthroughVerifier) VerifyArtifact(context.Context, []byte, plugin.RegistryVersionEntry) error {
	return nil
}

// resolveCandidateFetcher is version-agnostic: FetchArtifact always serves
// grantDiffCandidateManifestTOMLCLI (a real manifest whose own Version
// field is "1.1.0") regardless of the entry.Version it is asked for, so a
// test entry naming a version the manifest does not itself carry (e.g.
// "2.0.0") reaches resolveVerifiedCandidate's own version-mismatch check
// instead of dying earlier inside the fetcher on "no artifact staged" the
// way every other fetcher double in this package (keyed by version) would.
type resolveCandidateFetcher struct{}

func (resolveCandidateFetcher) FetchIndex(context.Context) ([]byte, error) { return nil, nil }

func (resolveCandidateFetcher) FetchArtifact(context.Context, plugin.RegistryVersionEntry) ([]byte, error) {
	return []byte(grantDiffCandidateManifestTOMLCLI), nil
}

// TestResolveVerifiedCandidate is the S-50.T8 confirming review's F2:
// resolveVerifiedCandidate's id-mismatch and version-mismatch branches
// (adversarial CR FIX-6) had zero coverage anywhere in the tree.
func TestResolveVerifiedCandidate(t *testing.T) {
	deps := updateTestDeps(t)
	client := plugin.NewRegistryClient(
		plugin.RegistryConfig{}, resolveCandidateFetcher{}, passthroughVerifier{}, nil, runtime.NewSystemClock(),
	)
	entry := plugin.RegistryVersionEntry{Version: "2.0.0", Checksum: "unused-passthrough-verifier-ignores-this"}

	t.Run("version_mismatch_refused", func(t *testing.T) {
		_, _, err := resolveVerifiedCandidate(context.Background(), deps, client, "grant-diff-demo", entry)
		if !cascade.HasKind(err, cascade.KindIntegrity) {
			t.Fatalf("err = %v, want KindIntegrity", err)
		}
		if !strings.Contains(err.Error(), "1.1.0") || !strings.Contains(err.Error(), "2.0.0") {
			t.Fatalf("err = %v, want it to name both the manifest version 1.1.0 and the registry entry version 2.0.0", err)
		}
	})

	t.Run("id_mismatch_refused", func(t *testing.T) {
		_, _, err := resolveVerifiedCandidate(context.Background(), deps, client, "other-name", entry)
		if !cascade.HasKind(err, cascade.KindIntegrity) {
			t.Fatalf("err = %v, want KindIntegrity", err)
		}
		if !strings.Contains(err.Error(), "grant-diff-demo") || !strings.Contains(err.Error(), "other-name") {
			t.Fatalf("err = %v, want it to name both the manifest id grant-diff-demo and the requested name other-name", err)
		}
	})
}
