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
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/plugin"
	"github.com/acamarata/cascade/pkg/plugin/registry/generator"
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

func signedRegistryTestIndex(t *testing.T, key ed25519.PrivateKey) []byte {
	t.Helper()
	payload := []byte(`{"schema_version":"1","entries":[]}`)
	idx := plugin.RegistryIndex{SchemaVersion: "1", Entries: []plugin.RegistryIndexEntry{}, Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(key, payload))}
	raw, err := json.Marshal(idx)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestPubkeyPathOverridesDefault(t *testing.T) {
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{31}, 32))
	other := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{32}, 32))
	old := defaultPluginRegistryPublicKey
	defaultPluginRegistryPublicKey = func() ed25519.PublicKey { return other.Public().(ed25519.PublicKey) }
	t.Cleanup(func() { defaultPluginRegistryPublicKey = old })
	path := filepath.Join(t.TempDir(), "override.pub")
	if err := os.WriteFile(path, []byte(base64.StdEncoding.EncodeToString(key.Public().(ed25519.PublicKey))), 0o600); err != nil {
		t.Fatal(err)
	}
	pub, warning := resolvePluginRegistryPubkey(path)
	if warning != "" || !bytes.Equal(pub, key.Public().(ed25519.PublicKey)) {
		t.Fatalf("override: %s", warning)
	}
	v := plugin.Ed25519Verifier{PublicKey: pub}
	if _, err := v.VerifyIndex(context.Background(), signedRegistryTestIndex(t, key)); err != nil {
		t.Fatal(err)
	}
	if _, err := v.VerifyIndex(context.Background(), signedRegistryTestIndex(t, other)); err == nil {
		t.Fatal("embedded key accepted under override")
	}
}

func TestUnreadableOverrideNeverFallsBack(t *testing.T) {
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{32}, 32))
	old := defaultPluginRegistryPublicKey
	defaultPluginRegistryPublicKey = func() ed25519.PublicKey { return key.Public().(ed25519.PublicKey) }
	t.Cleanup(func() { defaultPluginRegistryPublicKey = old })
	for _, contents := range []string{"missing", "not-base64!", base64.StdEncoding.EncodeToString(make([]byte, 31))} {
		dir := t.TempDir()
		for _, name := range []string{"HOME", "USERPROFILE", "CASCADE_HOME"} {
			t.Setenv(name, dir)
		}
		path := filepath.Join(dir, "override.pub")
		if contents != "missing" {
			if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		writePluginRegistryConfig(t, dir, "[registry]\nurl = 'https://registry.example'\npubkey_path = '"+path+"'\n")
		client, warning := buildPluginRegistryClient(context.Background(), fakeDaemonPaths{root: dir}, runtime.NewSystemClock())
		if client != nil || warning == "" {
			t.Fatalf("bad override returned client=%v warning=%q", client, warning)
		}
		pub, warning := resolvePluginRegistryPubkey(path)
		if pub != nil || warning == "" {
			t.Fatal("bad override resolved a key")
		}
		if _, err := (plugin.Ed25519Verifier{PublicKey: pub}).VerifyIndex(context.Background(), signedRegistryTestIndex(t, key)); err == nil {
			t.Fatal("refused state accepted index")
		}
	}
}

func TestEmbeddedDefaultVerifiesDerivedIndex(t *testing.T) {
	key, err := generator.DeriveRegistryKey(bytes.Repeat([]byte{29}, 32))
	if err != nil {
		t.Fatal(err)
	}
	old := defaultPluginRegistryPublicKey
	defaultPluginRegistryPublicKey = func() ed25519.PublicKey { return key.Public().(ed25519.PublicKey) }
	t.Cleanup(func() { defaultPluginRegistryPublicKey = old })
	pub, warning := resolvePluginRegistryPubkey("")
	if warning != "" {
		t.Fatal(warning)
	}
	v := plugin.Ed25519Verifier{PublicKey: pub}
	if _, err := v.VerifyIndex(context.Background(), signedRegistryTestIndex(t, key)); err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile("../../pkg/plugin/testdata/registry/index.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.VerifyIndex(context.Background(), fixture); err == nil {
		t.Fatal("fixture key accepted")
	}
	verifyEmbeddedRegistryClient(t, signedRegistryTestIndex(t, key), fixture)
}

// A canceled context forbids a network fetch: only a verified cache can answer.
func verifyEmbeddedRegistryClient(t *testing.T, valid, fixture []byte) {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"HOME", "USERPROFILE", "CASCADE_HOME"} {
		t.Setenv(name, dir)
	}
	cacheDir := filepath.Join(dir, "cache")
	writePluginRegistryConfig(t, dir, "[registry]\nurl = 'https://registry.example'\ncache_dir = '"+cacheDir+"'\n")
	clock := runtime.NewSystemClock()
	client, warning := buildPluginRegistryClient(context.Background(), fakeDaemonPaths{root: dir}, clock)
	if client == nil || warning != "" {
		t.Fatalf("client: %s", warning)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for i, raw := range [][]byte{valid, fixture} {
		envelope, err := json.Marshal(map[string]any{"cached_at": clock.Now(), "raw": raw})
		if err != nil {
			t.Fatal(err)
		}
		if err := (plugin.FileCache{Dir: cacheDir}).Put(context.Background(), "index", envelope); err != nil {
			t.Fatal(err)
		}
		_, err = client.Fetch(ctx)
		if (i == 0) != (err == nil) {
			t.Fatalf("cache acceptance %d: %v", i, err)
		}
	}
}

func TestBuildPluginRegistryClientURLNoPubkey(t *testing.T) {
	dir := t.TempDir()
	writePluginRegistryConfig(t, dir, "[registry]\nurl = \"https://registry.example\"\n")
	client, warning := buildPluginRegistryClient(context.Background(), fakeDaemonPaths{root: dir}, runtime.NewSystemClock())
	if client == nil {
		t.Fatalf("url with no pubkey_path: want embedded default, warning=%q", warning)
	}
	if warning != "" {
		t.Fatal(warning)
	}
}

func TestBuildPluginRegistryClientConfigured(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "registry.pub")
	writePluginRegistryPubkeyFixture(t, keyPath)
	// A TOML literal string ('...') carries a windows path verbatim; a basic
	// string would read its backslashes as escapes and the config would not
	// parse (the windows lane proved it).
	writePluginRegistryConfig(t, dir, "[registry]\nurl = \"https://registry.example\"\npubkey_path = '"+keyPath+"'\n")
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
	if len(pub) != 32 {
		t.Fatalf("empty path: want embedded 32-byte key")
	}
	if warning != "" {
		t.Fatal(warning)
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
