package runtime

// Purpose (this file): TestRegistryConfigKey* proves [registry] registers
// via Load, defaults CacheTTL when unset, parses every field, rejects an
// unknown key and a malformed duration, is hot-reloadable (never added to
// hotreload.go's coldSections), resolves CASCADE_REGISTRY__* through the
// generic env-override machinery (config_env.go), and surfaces in
// EffectiveEntries. Mirrors config_widget_test.go's exact structure.
// SPORT: runtime/config-registry (ADD, P1-E24-W5-S50-T2).

import (
	"context"
	"testing"
	"time"
)

func loadRegistryTestConfig(t *testing.T, toml string, environ map[string]string) *Config {
	t.Helper()
	dir := t.TempDir()
	path := writeConfigFile(t, dir, toml)
	cfg, err := Load(context.Background(), LoadOptions{Path: path, Getenv: func(string) string { return "" }, Environ: fakeEnviron(environ)})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return cfg
}

func TestRegistryConfigKeyDefaults(t *testing.T) {
	cfg := loadRegistryTestConfig(t, "", nil)
	if cfg.Registry.URL != "" {
		t.Errorf("URL = %q, want empty (unconfigured)", cfg.Registry.URL)
	}
	if cfg.Registry.CacheTTL != defaultRegistryCacheTTL {
		t.Errorf("CacheTTL = %v, want default %v", cfg.Registry.CacheTTL, defaultRegistryCacheTTL)
	}
	if cfg.Registry.PubkeyPath != "" {
		t.Errorf("PubkeyPath = %q, want empty — no in-binary default in this tree (see this file's header)", cfg.Registry.PubkeyPath)
	}
}

func TestRegistryConfigKeyExplicitFields(t *testing.T) {
	toml := "[registry]\nurl = \"https://registry.example\"\ncache_dir = \"/var/cache/cascade/registry\"\n" +
		"cache_ttl = \"30m\"\npubkey_path = \"/etc/cascade/registry.pub\"\n"
	cfg := loadRegistryTestConfig(t, toml, nil)
	if cfg.Registry.URL != "https://registry.example" {
		t.Errorf("URL = %q, want https://registry.example", cfg.Registry.URL)
	}
	if cfg.Registry.CacheDir != "/var/cache/cascade/registry" {
		t.Errorf("CacheDir = %q, want /var/cache/cascade/registry", cfg.Registry.CacheDir)
	}
	if cfg.Registry.CacheTTL != 30*time.Minute {
		t.Errorf("CacheTTL = %v, want 30m", cfg.Registry.CacheTTL)
	}
	if cfg.Registry.PubkeyPath != "/etc/cascade/registry.pub" {
		t.Errorf("PubkeyPath = %q, want /etc/cascade/registry.pub", cfg.Registry.PubkeyPath)
	}
}

func TestRegistryConfigKeyUnknownKeyError(t *testing.T) {
	dir := t.TempDir()
	path := writeConfigFile(t, dir, "[registry]\nbogus_key = 1\n")
	_, err := Load(context.Background(), LoadOptions{Path: path, Getenv: func(string) string { return "" }, Environ: fakeEnviron(nil)})
	var cfgErr *ConfigError
	if err == nil {
		t.Fatal("Load: want a *ConfigError for an unknown [registry] key, got nil")
	}
	if !asConfigError(err, &cfgErr) {
		t.Fatalf("Load error = %T, want *ConfigError", err)
	}
}

func TestRegistryConfigKeyMalformedDuration(t *testing.T) {
	dir := t.TempDir()
	path := writeConfigFile(t, dir, "[registry]\ncache_ttl = \"not-a-duration\"\n")
	_, err := Load(context.Background(), LoadOptions{Path: path, Getenv: func(string) string { return "" }, Environ: fakeEnviron(nil)})
	var cfgErr *ConfigError
	if err == nil {
		t.Fatal("Load: want a *ConfigError for a malformed cache_ttl, got nil")
	}
	if !asConfigError(err, &cfgErr) {
		t.Fatalf("Load error = %T, want *ConfigError", err)
	}
}

// TestRegistryConfigKeyEnvOverride proves D1's own requirement: the
// registry section resolves CASCADE_REGISTRY__URL and
// CASCADE_REGISTRY__PUBKEY_PATH through config_env.go's generic
// CASCADE_<SECTION>__<KEY> machinery — never a raw os.Getenv read inside
// a handler.
func TestRegistryConfigKeyEnvOverride(t *testing.T) {
	cfg := loadRegistryTestConfig(t, "", map[string]string{
		"CASCADE_REGISTRY__URL":         "https://env.example",
		"CASCADE_REGISTRY__PUBKEY_PATH": "/env/registry.pub",
	})
	if cfg.Registry.URL != "https://env.example" {
		t.Errorf("URL = %q, want the env override https://env.example", cfg.Registry.URL)
	}
	if cfg.Registry.PubkeyPath != "/env/registry.pub" {
		t.Errorf("PubkeyPath = %q, want the env override /env/registry.pub", cfg.Registry.PubkeyPath)
	}
	if cfg.Source("registry.url") != SourceEnv {
		t.Errorf("Source(registry.url) = %v, want SourceEnv", cfg.Source("registry.url"))
	}
}

func TestRegistryConfigKeyHotReloadClass(t *testing.T) {
	for _, section := range coldSections {
		if section == "registry" {
			t.Fatal("\"registry\" must not be in coldSections: [registry] is R-14.75's hot-reloadable section")
		}
	}
}

func TestRegistryConfigKeyEffectiveEntries(t *testing.T) {
	cfg := loadRegistryTestConfig(t, "[registry]\nurl = \"https://registry.example\"\n", nil)
	found := false
	for _, e := range cfg.EffectiveEntries() {
		if e.Key == "registry.url" {
			found = true
			if v, ok := e.Value.(string); !ok || v != "https://registry.example" {
				t.Errorf("registry.url effective value = %v, want https://registry.example", e.Value)
			}
		}
	}
	if !found {
		t.Error("EffectiveEntries: registry.url not present")
	}
}
