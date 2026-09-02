package runtime

import (
	"context"
	"fmt"
	"testing"
)

// Purpose: tests for config_logging.go's parseLoggingSection /
//   parseLoggingRotation / tomlInt, exercised through the public Load
//   entrypoint (config.go) — the R-14.107 rotation-defaults contract,
//   every level/format value, and every typed-error path. Split out of
//   logger_test.go per Art.10.3 (that file crossed 300 lines once this
//   suite was added); further split into one top-level func per case
//   per Art.10.3's 50-line function cap (funlen counts subtests inline).
// SPORT: runtime/logger (ADD, per T-2 sport_updates).

func TestLoad_LoggingSection_RotationDisabledWhenUnset(t *testing.T) {
	dir := t.TempDir()
	path := writeConfigFile(t, dir, "[logging]\nlevel = \"warn\"\nformat = \"text\"\n")
	cfg, err := Load(context.Background(), LoadOptions{Path: path, Getenv: func(string) string { return "" }, Environ: fakeEnviron(nil)})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Logging.Level != "warn" || cfg.Logging.Format != "text" {
		t.Errorf("Logging = %+v", cfg.Logging)
	}
	if cfg.Logging.Rotation.Enabled() {
		t.Error("Rotation.Enabled() = true with no [logging.rotation] table, want false (R-14.107)")
	}
}

// TestLoad_LoggingSection_RotationDisabledWithOnlyOneKeySet covers
// R-14.107's "either key alone" direction (NIT FIX 4, CR on
// P1-E03-W1-S04-T2): loggingRotation.Enabled() requires BOTH MaxSizeMB
// and MaxFiles to be non-nil, but only "neither set" and "both set" had
// tests — "only max_size_mb" and "only max_files" were unproven.
func TestLoad_LoggingSection_RotationDisabledWithOnlyOneKeySet(t *testing.T) {
	cases := map[string]string{
		"only max_size_mb": "[logging.rotation]\nmax_size_mb = 10\n",
		"only max_files":   "[logging.rotation]\nmax_files = 3\n",
	}
	for name, toml := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := writeConfigFile(t, dir, toml)
			cfg, err := Load(context.Background(), LoadOptions{Path: path, Getenv: func(string) string { return "" }, Environ: fakeEnviron(nil)})
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.Logging.Rotation.Enabled() {
				t.Errorf("Rotation.Enabled() = true with only one key set (%s), want false (R-14.107)", name)
			}
		})
	}
}

func TestLoad_LoggingSection_RotationEnabledWhenBothKeysSet(t *testing.T) {
	dir := t.TempDir()
	path := writeConfigFile(t, dir, "[logging.rotation]\nmax_size_mb = 10\nmax_files = 3\n")
	cfg, err := Load(context.Background(), LoadOptions{Path: path, Getenv: func(string) string { return "" }, Environ: fakeEnviron(nil)})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Logging.Rotation.Enabled() {
		t.Fatal("Rotation.Enabled() = false with both keys set")
	}
	if *cfg.Logging.Rotation.MaxSizeMB != 10 || *cfg.Logging.Rotation.MaxFiles != 3 {
		t.Errorf("Rotation = %+v", cfg.Logging.Rotation)
	}
}

func TestLoad_LoggingSection_ErrorPaths(t *testing.T) {
	errorCases := map[string]string{
		"level wrong type":      "[logging]\nlevel = 5\n",
		"level bad enum":        "[logging]\nlevel = \"verbose\"\n",
		"format wrong type":     "[logging]\nformat = 5\n",
		"format bad enum":       "[logging]\nformat = \"xml\"\n",
		"rotation not a table":  "[logging]\nrotation = 5\n",
		"rotation unknown key":  "[logging.rotation]\nmax_size = 10\n",
		"rotation non positive": "[logging.rotation]\nmax_size_mb = 0\nmax_files = 1\n",
		"rotation wrong type":   "[logging.rotation]\nmax_size_mb = \"big\"\nmax_files = 1\n",
	}
	for name, toml := range errorCases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := writeConfigFile(t, dir, toml)
			_, err := Load(context.Background(), LoadOptions{Path: path, Getenv: func(string) string { return "" }, Environ: fakeEnviron(nil)})
			if err == nil {
				t.Fatalf("Load(%q): want *ConfigError, got nil", toml)
			}
			if _, ok := err.(*ConfigError); !ok {
				t.Fatalf("Load(%q) error = %v (%T), want *ConfigError", toml, err, err)
			}
		})
	}
}

func TestLoad_LoggingSection_UnknownTopLevelKeyWarnsNotErrors(t *testing.T) {
	dir := t.TempDir()
	path := writeConfigFile(t, dir, "[logging]\nfuture_key = true\n")
	var warned []string
	_, err := Load(context.Background(), LoadOptions{
		Path:    path,
		Getenv:  func(string) string { return "" },
		Environ: fakeEnviron(nil),
		Warn:    func(format string, args ...interface{}) { warned = append(warned, fmt.Sprintf(format, args...)) },
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(warned) == 0 {
		t.Error("expected a warning for logging.future_key, got none")
	}
}
