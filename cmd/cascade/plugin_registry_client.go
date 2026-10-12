// Purpose: builds the *plugin.RegistryClient `plugin search`'s handler,
// its MCP tool, and cascade init step 4 all share (P1-E24-W5-S50-T2, D1/
// D6) — loading [registry] through internal/runtime.Config (never raw
// os.Getenv) and resolving the fail-closed states R-14.75 requires. Split
// out of plugin_search.go purely to stay under Art.10.3's 300-line cap.
//
// Inputs: a context, the resolved PathProvider, and a Clock.
// Outputs: a *plugin.RegistryClient, or nil with a human-readable warning
// string explaining why (empty string means "not configured", a normal,
// silent state — never warned about).
// Constraints: explicit key paths never fall back to the embedded trust root.
// SPORT: cli/plugin-search/ADD (P1-E24-W5-S50-T2).
package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/acamarata/cascade/internal/plugins/registryfetch"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/plugin"
)

// pluginRegistryCacheTTLFloor is used only when [registry].cache_ttl
// resolves to zero (should not happen — config_registry.go defaults it —
// kept as a last-resort floor so a zero TTL never means "always re-fetch,
// every call").
const pluginRegistryCacheTTLFloor = time.Minute

var defaultPluginRegistryPublicKey = plugin.DefaultRegistryPublicKey

// buildPluginRegistryClient loads [registry] once and returns a client,
// or nil with a human-readable warning explaining why not (D1). Three
// fail-closed outcomes, in order:
//
//  1. config.toml will not load at all: nil, warning.
//  2. [registry].url is empty: nil, no warning ("not configured" is a
//     normal, silent state).
//  3. [registry].url is set but no usable Ed25519 public key resolves
//     from pubkey_path: nil, warning naming R-14.75 and the doctor
//     finding this same gap reports.
func buildPluginRegistryClient(ctx context.Context, paths runtime.PathProvider, clock runtime.Clock) (*plugin.RegistryClient, string) {
	cfg, err := runtime.Load(ctx, runtime.LoadOptions{Path: paths.ConfigPath()})
	if err != nil {
		return nil, fmt.Sprintf("cascade: plugin.search: could not load config.toml (%v); falling back to the builtin plugin catalog", err)
	}
	if cfg.Registry.URL == "" {
		return nil, ""
	}
	pub, warning := resolvePluginRegistryPubkey(cfg.Registry.PubkeyPath)
	if pub == nil {
		return nil, warning
	}
	cacheDir := cfg.Registry.CacheDir
	if cacheDir == "" {
		cacheDir = filepath.Join(paths.DataDir(), "plugin-registry-cache")
	}
	cacheTTL := cfg.Registry.CacheTTL
	if cacheTTL <= 0 {
		cacheTTL = pluginRegistryCacheTTLFloor
	}
	return plugin.NewRegistryClient(
		plugin.RegistryConfig{RegistryURL: cfg.Registry.URL, CacheDir: cacheDir, CacheTTL: cacheTTL, PublicKey: pub},
		registryfetch.HTTPFetcher{BaseURL: cfg.Registry.URL},
		plugin.Ed25519Verifier{PublicKey: pub},
		plugin.FileCache{Dir: cacheDir},
		clock,
	), ""
}

// resolvePluginRegistryPubkey uses the embedded key only for an unset path.
// Every invalid explicit override refuses registry access with a warning.
func resolvePluginRegistryPubkey(pubkeyPath string) ([]byte, string) {
	if pubkeyPath == "" {
		return defaultPluginRegistryPublicKey(), ""
	}
	raw, err := os.ReadFile(pubkeyPath) //nolint:gosec // operator-configured path, same trust level as any config file
	if err != nil {
		return nil, fmt.Sprintf("cascade: plugin.search: [registry].pubkey_path %q could not be read (%v); "+
			"falling back to the builtin plugin catalog only", pubkeyPath, err)
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		return nil, fmt.Sprintf("cascade: plugin.search: [registry].pubkey_path %q is not valid base64 (%v); "+
			"falling back to the builtin plugin catalog only", pubkeyPath, err)
	}
	if len(decoded) != ed25519.PublicKeySize {
		return nil, "cascade: plugin.search: [registry].pubkey_path must contain a 32-byte Ed25519 public key; falling back to the builtin plugin catalog only"
	}
	return decoded, ""
}
