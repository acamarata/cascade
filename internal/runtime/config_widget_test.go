package runtime

// Purpose (this file): TestWidgetConfigKey* (named in P1-E38-W8-S74-T1's
// checks list — `-run TestWidgetConfigKey` matches every function below
// as a substring) proves [widget].show_project_names registers via Load,
// defaults to false, parses an explicit true, rejects an unknown key, and
// is hot-reloadable (never added to hotreload.go's coldSections). Split
// into five top-level functions (funlen's 50-line cap) rather than one
// function with five t.Run subtests.

import (
	"context"
	"testing"
)

func loadWidgetTestConfig(t *testing.T, toml string) *Config {
	t.Helper()
	dir := t.TempDir()
	path := writeConfigFile(t, dir, toml)
	cfg, err := Load(context.Background(), LoadOptions{Path: path, Getenv: func(string) string { return "" }, Environ: fakeEnviron(nil)})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return cfg
}

func TestWidgetConfigKeyDefaults(t *testing.T) {
	cfg := loadWidgetTestConfig(t, "")
	if cfg.Widget.ShowProjectNames != defaultShowProjectNames {
		t.Errorf("ShowProjectNames = %v, want default %v", cfg.Widget.ShowProjectNames, defaultShowProjectNames)
	}
}

func TestWidgetConfigKeyExplicitTrue(t *testing.T) {
	cfg := loadWidgetTestConfig(t, "[widget]\nshow_project_names = true\n")
	if !cfg.Widget.ShowProjectNames {
		t.Error("ShowProjectNames = false, want true")
	}
}

func TestWidgetConfigKeyUnknownKeyError(t *testing.T) {
	dir := t.TempDir()
	path := writeConfigFile(t, dir, "[widget]\nbogus_key = 1\n")
	_, err := Load(context.Background(), LoadOptions{Path: path, Getenv: func(string) string { return "" }, Environ: fakeEnviron(nil)})
	var cfgErr *ConfigError
	if err == nil {
		t.Fatal("Load: want a *ConfigError for an unknown [widget] key, got nil")
	}
	if !asConfigError(err, &cfgErr) {
		t.Fatalf("Load error = %T, want *ConfigError", err)
	}
}

func TestWidgetConfigKeyHotReloadClass(t *testing.T) {
	for _, section := range coldSections {
		if section == "widget" {
			t.Fatal("\"widget\" must not be in coldSections: [widget].show_project_names is R-21.200's hot-reloadable key")
		}
	}
}

func TestWidgetConfigKeyEffectiveEntries(t *testing.T) {
	cfg := loadWidgetTestConfig(t, "[widget]\nshow_project_names = true\n")
	found := false
	for _, e := range cfg.EffectiveEntries() {
		if e.Key == "widget.show_project_names" {
			found = true
			if v, ok := e.Value.(bool); !ok || !v {
				t.Errorf("widget.show_project_names effective value = %v, want true", e.Value)
			}
		}
	}
	if !found {
		t.Error("EffectiveEntries: widget.show_project_names not present")
	}
}
