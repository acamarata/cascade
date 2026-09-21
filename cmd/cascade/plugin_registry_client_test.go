// Purpose: direct unit tests for plugin_registry_client.go's fail-closed
// resolution logic (D1) — the three buildPluginRegistryClient outcomes and
// resolvePluginRegistryPubkey's own three branches (empty path, unreadable
// file, bad base64), none of which plugin_search_catalog_test.go's
// higher-level pluginCatalogSearch tests exercise directly (those inject
// an already-built client).
//
// SPORT: cli/plugin-search/ADD (P1-E24-W5-S50-T2).
package main

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/internal/runtime"
)

func TestBuildPluginRegistryClientNotConfigured(t *testing.T) {
	paths := fakeDaemonPaths{root: t.TempDir()}
	client, warning := buildPluginRegistryClient(context.Background(), paths, runtime.NewSystemClock())
	if client != nil {
		t.Fatalf("no config.toml at all: client = %v, want nil", client)
	}
	if warning != "" {
		t.Fatalf("unconfigured is a normal, silent state: warning = %q, want empty", warning)
	}
}

func TestBuildPluginRegistryClientURLNoPubkey(t *testing.T) {
	dir := t.TempDir()
	writePluginRegistryConfig(t, dir, "[registry]\nurl = \"https://registry.example\"\n")
	client, warning := buildPluginRegistryClient(context.Background(), fakeDaemonPaths{root: dir}, runtime.NewSystemClock())
	if client != nil {
		t.Fatalf("url with no pubkey_path: client = %v, want nil (fail closed)", client)
	}
	if warning == "" {
		t.Fatal("url with no pubkey_path: want a non-empty warning naming R-14.75")
	}
}

func TestBuildPluginRegistryClientConfigured(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "registry.pub")
	writePluginRegistryPubkeyFixture(t, keyPath)
	writePluginRegistryConfig(t, dir, "[registry]\nurl = \"https://registry.example\"\npubkey_path = \""+keyPath+"\"\n")
	client, warning := buildPluginRegistryClient(context.Background(), fakeDaemonPaths{root: dir}, runtime.NewSystemClock())
	if client == nil {
		t.Fatalf("url+valid pubkey: client = nil, warning = %q, want a real client", warning)
	}
	if warning != "" {
		t.Errorf("url+valid pubkey: warning = %q, want empty", warning)
	}
}

func TestResolvePluginRegistryPubkeyEmptyPath(t *testing.T) {
	pub, warning := resolvePluginRegistryPubkey("")
	if pub != nil {
		t.Fatalf("empty path: pub = %v, want nil", pub)
	}
	if warning == "" {
		t.Fatal("empty path: want a non-empty R-14.75 warning")
	}
}

func TestResolvePluginRegistryPubkeyUnreadableFile(t *testing.T) {
	pub, warning := resolvePluginRegistryPubkey(filepath.Join(t.TempDir(), "does-not-exist.pub"))
	if pub != nil {
		t.Fatalf("unreadable file: pub = %v, want nil", pub)
	}
	if warning == "" {
		t.Fatal("unreadable file: want a non-empty warning")
	}
}

func TestResolvePluginRegistryPubkeyBadBase64(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.pub")
	writePluginRegistryConfig(t, dir, "") // ensure dir exists
	if err := os.WriteFile(path, []byte("not-valid-base64!!!"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	pub, warning := resolvePluginRegistryPubkey(path)
	if pub != nil {
		t.Fatalf("bad base64: pub = %v, want nil", pub)
	}
	if warning == "" {
		t.Fatal("bad base64: want a non-empty warning")
	}
}

func TestResolvePluginRegistryPubkeyValid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "good.pub")
	writePluginRegistryPubkeyFixture(t, path)
	pub, warning := resolvePluginRegistryPubkey(path)
	if pub == nil {
		t.Fatalf("valid key: pub = nil, warning = %q, want 32 bytes", warning)
	}
	if len(pub) != 32 {
		t.Errorf("valid key: len(pub) = %d, want 32 (Ed25519 public key)", len(pub))
	}
}

// writePluginRegistryConfig writes a minimal config.toml under dir/
// config.toml, matching fakeDaemonPaths.ConfigPath()'s own join.
func writePluginRegistryConfig(t *testing.T, dir, toml string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(toml), 0o600); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}
}

// writePluginRegistryPubkeyFixture writes a real, syntactically valid
// (though not a production trust root — see config_registry.go's header)
// base64-encoded 32-byte Ed25519 public key to path.
func writePluginRegistryPubkeyFixture(t *testing.T, path string) {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	if err := os.WriteFile(path, []byte(base64.StdEncoding.EncodeToString(key)), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
}
