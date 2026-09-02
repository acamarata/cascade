package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Purpose: tests for the config.toml file-I/O and section-parsing
//   behaviour in config_load.go (readAndUpgradeTree, parseElevationSection,
//   resolveRuntimeSection, extraSections, writeConfigAtomic), exercised
//   through the public Load entry point. Split out of a single
//   config_test.go per R-14.117 (Art.10.3 file-cap remedy) —
//   behaviour-preserving, moved code only.
// Inputs: n/a (test-only).
// Outputs: n/a (test-only).
// Constraints: Art.7.1 — every test uses t.TempDir() and injected
//   Getenv/Environ, never the real process environment or $HOME.
// SPORT: runtime/config (ADD, placeholder per T-1 sport_updates).

func TestLoad_SchemaUpgradeWriteFailureIsTypedError(t *testing.T) {
	// A config file that needs a schema upgrade, sitting inside a
	// directory Load has no write permission to: os.CreateTemp inside
	// writeConfigAtomic must fail, and Load must surface that failure
	// rather than silently dropping the upgrade.
	dir := t.TempDir()
	roDir := filepath.Join(dir, "readonly")
	if err := os.Mkdir(roDir, 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	cfgPath := writeConfigFile(t, roDir, "[runtime]\nprofile = \"local\"\n")
	if err := os.Chmod(roDir, 0o555); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	defer func() { _ = os.Chmod(roDir, 0o755) }() // allow t.TempDir() cleanup to succeed

	_, err := Load(context.Background(), LoadOptions{
		Path:    cfgPath,
		Getenv:  func(string) string { return "" },
		Environ: fakeEnviron(nil),
	})
	if err == nil {
		t.Fatal("expected a write error from a read-only config directory, got nil")
	}
}

func TestLoad_MissingFileFallsBackToDefaults(t *testing.T) {
	dir := t.TempDir()
	cfg, err := Load(context.Background(), LoadOptions{
		Path:    filepath.Join(dir, "config.toml"),
		Getenv:  func(string) string { return "" },
		Environ: fakeEnviron(nil),
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Runtime.Profile != DefaultProfile {
		t.Errorf("Profile = %q, want default %q", cfg.Runtime.Profile, DefaultProfile)
	}
	if cfg.Source("runtime.profile") != SourceDefault {
		t.Errorf("Source(runtime.profile) = %q, want default", cfg.Source("runtime.profile"))
	}
	if cfg.SchemaVersion != CurrentSchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", cfg.SchemaVersion, CurrentSchemaVersion)
	}
}

func TestLoad_MalformedTOMLTypedError(t *testing.T) {
	dir := t.TempDir()
	path := writeConfigFile(t, dir, "this is not [ valid toml")
	_, err := Load(context.Background(), LoadOptions{
		Path:    path,
		Getenv:  func(string) string { return "" },
		Environ: fakeEnviron(nil),
	})
	if err == nil {
		t.Fatal("expected an error for malformed TOML, got nil")
	}
	var ce *ConfigError
	if !errors.As(err, &ce) {
		t.Fatalf("error = %v, want *ConfigError", err)
	}
}

func TestLoad_UnknownRuntimeKeyWarnsNotErrors(t *testing.T) {
	dir := t.TempDir()
	path := writeConfigFile(t, dir, "[runtime]\nprofile = \"local\"\nbogus_key = \"x\"\n")
	var warnings []string
	cfg, err := Load(context.Background(), LoadOptions{
		Path:    path,
		Getenv:  func(string) string { return "" },
		Environ: fakeEnviron(nil),
		Warn: func(format string, args ...interface{}) {
			warnings = append(warnings, fmt.Sprintf(format, args...))
		},
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Runtime.Profile != ProfileLocal {
		t.Errorf("Profile = %q, want local", cfg.Runtime.Profile)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "bogus_key") {
		t.Errorf("warnings = %v, want exactly one mentioning bogus_key", warnings)
	}
}

// TestLoad_DerivedRuntimeKeysWarn covers runtime.home and runtime.data_dir,
// which are derived from the path layout and never read from the file. They
// were previously matched as known keys and then discarded in total silence,
// which reads to a user as if the value had been applied — worse than the
// warning any unrecognised key already produced.
func TestLoad_DerivedRuntimeKeysWarn(t *testing.T) {
	for _, key := range []string{"home", "data_dir"} {
		t.Run(key, func(t *testing.T) {
			dir := t.TempDir()
			path := writeConfigFile(t, dir,
				"[runtime]\nprofile = \"local\"\n"+key+" = \"/somewhere/else\"\n")
			var warnings []string
			cfg, err := Load(context.Background(), LoadOptions{
				Path:    path,
				Getenv:  func(string) string { return "" },
				Environ: fakeEnviron(nil),
				Warn: func(format string, args ...interface{}) {
					warnings = append(warnings, fmt.Sprintf(format, args...))
				},
			})
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if len(warnings) != 1 || !strings.Contains(warnings[0], key) {
				t.Fatalf("warnings = %v, want exactly one mentioning %q", warnings, key)
			}
			if !strings.Contains(warnings[0], "ignored") {
				t.Errorf("warning %q does not say the value is ignored", warnings[0])
			}
			// And it must genuinely not take effect.
			if cfg.Runtime.Home == "/somewhere/else" || cfg.Runtime.DataDir == "/somewhere/else" {
				t.Errorf("file value was applied to Runtime: %+v", cfg.Runtime)
			}
		})
	}
}

func TestLoad_ElevationUnrecognisedKeyHardErrors(t *testing.T) {
	dir := t.TempDir()
	path := writeConfigFile(t, dir, "[elevation]\nallow_remote = false\nbogus_key = \"x\"\n")
	_, err := Load(context.Background(), LoadOptions{
		Path:    path,
		Getenv:  func(string) string { return "" },
		Environ: fakeEnviron(nil),
	})
	if err == nil {
		t.Fatal("expected a hard error for an unrecognised [elevation] key, got nil")
	}
	var ce *ConfigError
	if !errors.As(err, &ce) || ce.Field != "elevation.bogus_key" {
		t.Fatalf("error = %v, want *ConfigError{Field: elevation.bogus_key}", err)
	}
}

func TestLoad_ElevationAllowRemoteRequiresPubkey(t *testing.T) {
	dir := t.TempDir()
	path := writeConfigFile(t, dir, "[elevation]\nallow_remote = true\n")
	_, err := Load(context.Background(), LoadOptions{
		Path:    path,
		Getenv:  func(string) string { return "" },
		Environ: fakeEnviron(nil),
	})
	if err == nil {
		t.Fatal("expected a missing-required-field error, got nil")
	}
	var ce *ConfigError
	if !errors.As(err, &ce) || ce.Field != "elevation.helper_pubkey" {
		t.Fatalf("error = %v, want *ConfigError{Field: elevation.helper_pubkey}", err)
	}
}

func TestLoad_ElevationTypeMismatch(t *testing.T) {
	dir := t.TempDir()
	path := writeConfigFile(t, dir, "[elevation]\nallow_remote = \"not-a-bool\"\n")
	_, err := Load(context.Background(), LoadOptions{
		Path:    path,
		Getenv:  func(string) string { return "" },
		Environ: fakeEnviron(nil),
	})
	if err == nil {
		t.Fatal("expected a type-mismatch error, got nil")
	}
}

func TestLoad_ElevationColdSectionRoundTrips(t *testing.T) {
	dir := t.TempDir()
	path := writeConfigFile(t, dir, "[elevation]\nallow_remote = true\nhelper_pubkey = \"abc123\"\n")
	cfg, err := Load(context.Background(), LoadOptions{
		Path:    path,
		Getenv:  func(string) string { return "" },
		Environ: fakeEnviron(nil),
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Elevation.AllowRemote || cfg.Elevation.HelperPubkey != "abc123" {
		t.Errorf("Elevation = %+v", cfg.Elevation)
	}
}

func TestLoad_ExtraSectionsPreservedNotWarned(t *testing.T) {
	dir := t.TempDir()
	path := writeConfigFile(t, dir, "[logging]\nlevel = \"debug\"\n\n[storage]\ndriver = \"sqlite\"\n")
	var warnings []string
	cfg, err := Load(context.Background(), LoadOptions{
		Path:    path,
		Getenv:  func(string) string { return "" },
		Environ: fakeEnviron(nil),
		Warn: func(format string, args ...interface{}) {
			warnings = append(warnings, fmt.Sprintf(format, args...))
		},
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none (unowned sections are preserved silently)", warnings)
	}
	logging, ok := cfg.Extra["logging"].(map[string]interface{})
	if !ok || logging["level"] != "debug" {
		t.Errorf("Extra[logging] = %#v", cfg.Extra["logging"])
	}
}

func TestLoad_SchemaUpgradeRewritesFileAtomicallyAndIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := writeConfigFile(t, dir, "[runtime]\nprofile = \"local\"\n")

	cfg1, err := Load(context.Background(), LoadOptions{Path: path, Getenv: func(string) string { return "" }, Environ: fakeEnviron(nil)})
	if err != nil {
		t.Fatalf("Load (first): %v", err)
	}
	if cfg1.SchemaVersion != CurrentSchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", cfg1.SchemaVersion, CurrentSchemaVersion)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read rewritten config: %v", err)
	}
	if !strings.Contains(string(data), "schema_version") {
		t.Errorf("rewritten config missing schema_version: %s", data)
	}

	// Second load against the now-current file must not mutate it again.
	cfg2, err := Load(context.Background(), LoadOptions{Path: path, Getenv: func(string) string { return "" }, Environ: fakeEnviron(nil)})
	if err != nil {
		t.Fatalf("Load (second): %v", err)
	}
	data2, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config after second load: %v", err)
	}
	if string(data) != string(data2) {
		t.Errorf("second Load mutated the file; not idempotent:\nfirst:\n%s\nsecond:\n%s", data, data2)
	}
	if cfg2.Runtime.Profile != ProfileLocal {
		t.Errorf("Profile after reload = %q, want local", cfg2.Runtime.Profile)
	}
}

func TestLoad_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Load(ctx, LoadOptions{Path: "", Getenv: func(string) string { return "" }, Environ: fakeEnviron(nil)})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}
