// Purpose: TestPluginUpdateCommand — CLI-facing tests for the X/S-50.T8
// registry-driven `cascade plugin update [name]` path
// (plugin_update_registry.go): no-update-available idempotency, a no-diff
// update actually committing to the embedded store, the
// --yes/--accept-grant grant-expansion gate (both directions),
// CASCADE_NO_INPUT=1's hard stop, and the daemon-configured refusal (D9
// parity — see plugin_search_test.go's TestPluginSearchDaemonDown, the
// precedent this file's TestPluginUpdateDaemonConfigured mirrors).
//
// Reuses the real provenance-stamped fixture at
// pkg/plugin/testdata/update/index-bump-with-new-grant.json (see that
// directory's README.md) via a relative path, rather than a second,
// undocumented signing key — the public key constant below is that same
// fixture's key, re-declared (not imported: cmd/cascade and pkg/plugin's
// external test package are different packages, and every existing
// fixture-key constant in this tree — testRegistryPublicKeyB64,
// altPublicKey — is likewise redeclared per file rather than shared
// across a package boundary).
//
// SPORT: cli/plugin-lifecycle/CHANGE tests (P1-E24-W5-S50-T8).
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

const grantDiffFixturePublicKeyB64CLI = "C7w0aldmfDgBIL2cf9flHSxf3+o3zS9b9AWyxr9vLXg="

const grantDiffCandidateManifestTOMLCLI = `schema = "cascade.plugin/v2"
id = "grant-diff-demo"
name = "Grant Diff Demo"
version = "1.1.0"
host_version = ">=1.0.0"
runtime = "builtin"
requires = ["storage.local", "network.egress"]
`

// cliUpdateFakeFetcher answers FetchIndex from the real committed fixture
// and FetchArtifact with the matching in-memory candidate manifest — no
// "net"/"net/http" import anywhere in this file (Art.7 §2).
type cliUpdateFakeFetcher struct{ index []byte }

func (f cliUpdateFakeFetcher) FetchIndex(context.Context) ([]byte, error) { return f.index, nil }

func (f cliUpdateFakeFetcher) FetchArtifact(_ context.Context, entry plugin.RegistryVersionEntry) ([]byte, error) {
	if entry.Version == "1.1.0" {
		return []byte(grantDiffCandidateManifestTOMLCLI), nil
	}
	return nil, cascade.Newf(cascade.KindNotFound, "fixture: no artifact staged for version %q", entry.Version)
}

func cliUpdateFakeClient(t *testing.T) *plugin.RegistryClient {
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
		plugin.RegistryConfig{}, cliUpdateFakeFetcher{index: data},
		plugin.Ed25519Verifier{PublicKey: ed25519.PublicKey(pub)}, nil, runtime.NewSystemClock(),
	)
}

// pluginUpdateBuiltin defaults rec's RuntimeMode to "builtin" when unset —
// every registry-path test seeds an installed record before calling
// UpdatePlugin, which (S-50.T8 rework) now refuses ANY runtime-tier
// CHANGE, not only a grant expansion (adversarial CR FIX-3). The real
// grant-diff-demo candidate manifest is runtime="builtin", so a seeded
// record must also carry RuntimeMode builtin to represent what a real
// prior `plugin add` would have recorded — otherwise every "no tier
// change" test here would spuriously look like one.
func pluginUpdateBuiltin(rec plugins.PluginMetadata) plugins.PluginMetadata {
	if rec.RuntimeMode == "" {
		rec.RuntimeMode = plugin.RuntimeBuiltin
	}
	return rec
}

func seedPluginUpdateMetadata(t *testing.T, deps pluginDeps, rec plugins.PluginMetadata) {
	t.Helper()
	store, closeStore, err := openPluginStore(context.Background(), deps.Paths, deps.Clock)
	if err != nil {
		t.Fatalf("openPluginStore (seed): %v", err)
	}
	defer closeStore()
	if err := plugins.SaveMetadata(context.Background(), store, rec); err != nil {
		t.Fatalf("seed SaveMetadata: %v", err)
	}
}

func loadPluginUpdateMetadata(t *testing.T, deps pluginDeps, name string) plugins.PluginMetadata {
	t.Helper()
	store, closeStore, err := openPluginStore(context.Background(), deps.Paths, deps.Clock)
	if err != nil {
		t.Fatalf("openPluginStore (load): %v", err)
	}
	defer closeStore()
	rec, ok, err := plugins.LoadMetadata(context.Background(), store, name)
	if err != nil {
		t.Fatalf("LoadMetadata(%s): %v", name, err)
	}
	if !ok {
		t.Fatalf("LoadMetadata(%s): not found", name)
	}
	return rec
}

func updateTestDeps(t *testing.T) pluginDeps {
	t.Helper()
	deps := testPluginDeps(t)
	deps.UpdateRegistryClient = func(context.Context) (*plugin.RegistryClient, error) {
		return cliUpdateFakeClient(t), nil
	}
	return deps
}

func TestPluginUpdateCommand(t *testing.T) {
	t.Run("no_update_available", testPluginUpdateNoneAvailable)
	t.Run("no_diff_update_commits", testPluginUpdateNoDiffCommits)
	t.Run("yes_without_accept_grant_fails_and_does_not_commit", testPluginUpdateYesWithoutAcceptGrantFails)
	t.Run("yes_with_accept_grant_still_refused_without_daemon", testPluginUpdateYesWithAcceptGrantStillRefusedWithoutDaemon)
	t.Run("no_input_without_yes_hard_errors", testPluginUpdateNoInputHardErrors)
	t.Run("no_input_without_yes_but_no_diff_proceeds", testPluginUpdateNoInputNoDiffProceeds)
	t.Run("tampered_artifact_refuses_and_leaves_store", testPluginUpdateTamperedArtifactRefusesAndLeavesStore)
	t.Run("removal_only_grant_diff_shown_and_committed", testPluginUpdateRemovalOnlyDiffShown)
	t.Run("not_installed_refuses", testPluginUpdateNotInstalled)
}

func testPluginUpdateNoneAvailable(t *testing.T) {
	deps := updateTestDeps(t)
	seedPluginUpdateMetadata(t, deps, pluginUpdateBuiltin(plugins.PluginMetadata{Name: "grant-diff-demo", InstalledVersion: "1.1.0", Grants: []string{"storage.local", "network.egress"}}))
	stdout, stderr, err := runPluginCLI(t, deps, "update", "grant-diff-demo")
	if err != nil {
		t.Fatalf("update at latest: unexpected error: %v (stderr=%s)", err, stderr)
	}
	if !strings.Contains(stdout, "no update available") {
		t.Fatalf("stdout = %q, want it to contain %q", stdout, "no update available")
	}
}

func testPluginUpdateNoDiffCommits(t *testing.T) {
	deps := updateTestDeps(t)
	seedPluginUpdateMetadata(t, deps, pluginUpdateBuiltin(plugins.PluginMetadata{Name: "grant-diff-demo", InstalledVersion: "1.0.0", Grants: []string{"storage.local", "network.egress"}}))
	stdout, stderr, err := runPluginCLI(t, deps, "update", "grant-diff-demo")
	if err != nil {
		t.Fatalf("no-diff update: unexpected error: %v (stderr=%s)", err, stderr)
	}
	if !strings.Contains(stdout, "updated grant-diff-demo to v1.1.0") {
		t.Fatalf("stdout = %q, want the updated message", stdout)
	}
	rec := loadPluginUpdateMetadata(t, deps, "grant-diff-demo")
	if rec.InstalledVersion != "1.1.0" {
		t.Fatalf("stored InstalledVersion = %q, want 1.1.0", rec.InstalledVersion)
	}
}

func testPluginUpdateYesWithoutAcceptGrantFails(t *testing.T) {
	deps := updateTestDeps(t)
	seedPluginUpdateMetadata(t, deps, pluginUpdateBuiltin(plugins.PluginMetadata{Name: "grant-diff-demo", InstalledVersion: "1.0.0", Grants: []string{"storage.local"}}))
	_, stderr, err := runPluginCLI(t, deps, "update", "grant-diff-demo", "--yes")
	if err == nil {
		t.Fatal("--yes without --accept-grant on a grant expansion = nil error, want non-nil")
	}
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("err = %v, want KindInvalidInput", err)
	}
	if !strings.Contains(err.Error(), "network.egress") && !strings.Contains(stderr, "network.egress") {
		t.Fatalf("err/stderr do not name the missing grant: err=%v stderr=%q", err, stderr)
	}
	rec := loadPluginUpdateMetadata(t, deps, "grant-diff-demo")
	if rec.InstalledVersion != "1.0.0" {
		t.Fatalf("stored InstalledVersion = %q after a refused update, want unchanged 1.0.0", rec.InstalledVersion)
	}
}

// noInputGetenv is a runtime.Getenv double that reports CASCADE_NO_INPUT=1
// and nothing else — no real process environment is ever read (Art.7).
func noInputGetenv(k string) string {
	if k == "CASCADE_NO_INPUT" {
		return "1"
	}
	return ""
}

func testPluginUpdateNoInputHardErrors(t *testing.T) {
	deps := updateTestDeps(t)
	deps.Getenv = noInputGetenv
	seedPluginUpdateMetadata(t, deps, pluginUpdateBuiltin(plugins.PluginMetadata{Name: "grant-diff-demo", InstalledVersion: "1.0.0", Grants: []string{"storage.local"}}))
	_, _, err := runPluginCLI(t, deps, "update", "grant-diff-demo")
	if err == nil {
		t.Fatal("CASCADE_NO_INPUT=1 without --yes on a grant expansion = nil error, want the hard-error refusal")
	}
	if !strings.Contains(err.Error(), "CASCADE_NO_INPUT=1 hard-errors") {
		t.Fatalf("err = %v, want the exact ErrNoInputHardError wording", err)
	}
	rec := loadPluginUpdateMetadata(t, deps, "grant-diff-demo")
	if rec.InstalledVersion != "1.0.0" {
		t.Fatalf("stored InstalledVersion = %q after a NO_INPUT refusal, want unchanged 1.0.0", rec.InstalledVersion)
	}
}

// testPluginUpdateNoInputNoDiffProceeds is §5.9: "no diff -> no
// re-confirmation required" means NO_INPUT's hard stop never fires when
// there is nothing to confirm.
func testPluginUpdateNoInputNoDiffProceeds(t *testing.T) {
	deps := updateTestDeps(t)
	deps.Getenv = noInputGetenv
	seedPluginUpdateMetadata(t, deps, pluginUpdateBuiltin(plugins.PluginMetadata{Name: "grant-diff-demo", InstalledVersion: "1.0.0", Grants: []string{"storage.local", "network.egress"}}))
	_, stderr, err := runPluginCLI(t, deps, "update", "grant-diff-demo")
	if err != nil {
		t.Fatalf("NO_INPUT with no grant diff: unexpected error: %v (stderr=%s)", err, stderr)
	}
}

// TestPluginUpdateDaemonConfigured mirrors plugin_search_test.go's
// TestPluginSearchDaemonDown (D9): a daemon CONFIGURED for this process
// (an explicit [daemon].socket entry in config.toml) must refuse the
// registry-driven update path rather than silently taking a local network
// path — see plugin_update_registry.go's errRegistryUpdateDaemonConfigured
// doc comment for why no RPC round trip exists to route through instead.
// deps.UpdateRegistryClient is deliberately left nil here so
// productionUpdateRegistryClient runs for real; it returns before any
// dial, so this stays inside the no-network-unit-lane (Art.7 §2).
func TestPluginUpdateDaemonConfigured(t *testing.T) {
	deps := testPluginDeps(t)
	if err := os.WriteFile(deps.Paths.ConfigPath(), []byte("[daemon]\nsocket = \"/nonexistent/daemon.sock\"\n"), 0o600); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}
	seedPluginUpdateMetadata(t, deps, pluginUpdateBuiltin(plugins.PluginMetadata{Name: "grant-diff-demo", InstalledVersion: "1.0.0", Grants: []string{"storage.local"}}))
	_, _, err := runPluginCLI(t, deps, "update", "grant-diff-demo")
	if err == nil {
		t.Fatal("update with a configured daemon = nil error, want the daemon-configured refusal")
	}
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("err = %v, want KindUnavailable", err)
	}
	if !strings.Contains(err.Error(), "daemon is configured") {
		t.Fatalf("err = %v, want it to name the daemon-configured refusal", err)
	}
}
