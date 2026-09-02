package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Purpose: config_load.go's schema-upgrade-on-read tests, split out of
//   config_load_test.go per R-14.117/Art.10.3 (300-line file cap) — this
//   ticket's own CR fix pushed that file over the cap by replacing two
//   tests with longer, more explicit byte-identical assertions.
// Inputs: n/a (test-only).
// Outputs: n/a (test-only).
// Constraints: Art.7.1 — every test uses t.TempDir() and injected
//   Getenv/Environ, never the real process environment or $HOME.
// SPORT: runtime/config (ADD, placeholder per T-1 sport_updates).

// TestLoad_ReadOnlyConfigDirNeverFails covers the same read-only-directory
// scenario the pre-fix TestLoad_SchemaUpgradeWriteFailureIsTypedError
// exercised, but with the corrected expectation: since Load never writes
// config.toml (readAndUpgradeTree's R-14 CR fix, P1-E03-W1-S05-T8,
// blocking fix 1), a legacy file needing a schema upgrade loads
// successfully even when its directory is read-only — there is no write
// attempt left to fail.
func TestLoad_ReadOnlyConfigDirNeverFails(t *testing.T) {
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

	cfg, err := Load(context.Background(), LoadOptions{
		Path:    cfgPath,
		Getenv:  func(string) string { return "" },
		Environ: fakeEnviron(nil),
	})
	if err != nil {
		t.Fatalf("Load on a read-only config dir must not fail (no write is ever attempted): %v", err)
	}
	if cfg.SchemaVersion != CurrentSchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d (upgraded in memory)", cfg.SchemaVersion, CurrentSchemaVersion)
	}
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config after Load: %v", err)
	}
	if strings.Contains(string(data), "schema_version") {
		t.Errorf("Load wrote schema_version to disk from a read path: %s", data)
	}
}

// TestLoad_SchemaUpgradeIsInMemoryOnlyAndNeverWritesTheFile is the
// corrected replacement for the pre-fix
// TestLoad_SchemaUpgradeRewritesFileAtomicallyAndIsIdempotent, which
// asserted the very defect this fix removes (a read verb rewriting
// config.toml to disk). R-14 CR (P1-E03-W1-S05-T8, blocking fix 1):
// UpgradeSchema still runs against the in-memory tree on every Load — so
// callers see the current SchemaVersion immediately — but the result is
// never persisted. The file on disk must be byte-identical across
// repeated Loads.
func TestLoad_SchemaUpgradeIsInMemoryOnlyAndNeverWritesTheFile(t *testing.T) {
	dir := t.TempDir()
	const original = "[runtime]\nprofile = \"local\"\n"
	path := writeConfigFile(t, dir, original)

	cfg1, err := Load(context.Background(), LoadOptions{Path: path, Getenv: func(string) string { return "" }, Environ: fakeEnviron(nil)})
	if err != nil {
		t.Fatalf("Load (first): %v", err)
	}
	if cfg1.SchemaVersion != CurrentSchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d (computed in memory)", cfg1.SchemaVersion, CurrentSchemaVersion)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config after first load: %v", err)
	}
	if string(data) != original {
		t.Errorf("Load rewrote config.toml from a read path:\nwant:\n%s\ngot:\n%s", original, data)
	}

	// A second Load must be equally non-mutating.
	cfg2, err := Load(context.Background(), LoadOptions{Path: path, Getenv: func(string) string { return "" }, Environ: fakeEnviron(nil)})
	if err != nil {
		t.Fatalf("Load (second): %v", err)
	}
	data2, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config after second load: %v", err)
	}
	if string(data2) != original {
		t.Errorf("second Load rewrote config.toml:\nwant:\n%s\ngot:\n%s", original, data2)
	}
	if cfg2.Runtime.Profile != ProfileLocal {
		t.Errorf("Profile after reload = %q, want local", cfg2.Runtime.Profile)
	}
}
