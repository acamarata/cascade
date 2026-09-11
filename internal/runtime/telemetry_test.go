package runtime

// Purpose: tests for telemetry.go's [telemetry] config stub — the
//   shipped default-off resolution, the hot reload class, the
//   never-force-enable rule across both env paths, and the typed-error
//   path for an invalid [telemetry] value.
// SPORT: runtime/config-telemetry (ADD, per T-3 sport_updates).

import (
	"testing"
)

// TestTelemetryDefaultOff covers this ticket's first AC: a fresh config
// (no [telemetry] table, no environment input) resolves
// telemetry.enabled=false, and [telemetry] registers in the hot reload
// class rather than the cold one.
func TestTelemetryDefaultOff(t *testing.T) {
	dir := t.TempDir()
	path := writeConfigFile(t, dir, "schema_version = 1\n")
	tree, _, err := readAndUpgradeTree(path)
	if err != nil {
		t.Fatalf("readAndUpgradeTree: %v", err)
	}

	cfg, err := ResolveTelemetryConfig(tree, fakeEnviron(nil), func(string) string { return "" }, nil)
	if err != nil {
		t.Fatalf("ResolveTelemetryConfig: %v", err)
	}
	if cfg.Enabled {
		t.Error("Enabled = true with no [telemetry] table and no environment input, want false (shipped default off, 08 §2)")
	}

	for _, s := range coldSections {
		if s == "telemetry" {
			t.Fatal("telemetry is listed in coldSections; it must stay in the hot reload class (08 §3 row)")
		}
	}
}

// testTelemetryNamedVarForceDisables is
// TestTelemetryHardDisableNeverForceEnable's first subtest, split out
// under the 50-line funlen cap: CASCADE_TELEMETRY=0 must force-disable
// even over a [telemetry] enabled=true file value.
func testTelemetryNamedVarForceDisables(t *testing.T) {
	dir := t.TempDir()
	path := writeConfigFile(t, dir, "[telemetry]\nenabled = true\n")
	tree, _, err := readAndUpgradeTree(path)
	if err != nil {
		t.Fatalf("readAndUpgradeTree: %v", err)
	}
	cfg, err := ResolveTelemetryConfig(tree, fakeEnviron(nil), func(k string) string {
		if k == "CASCADE_TELEMETRY" {
			return "0"
		}
		return ""
	}, nil)
	if err != nil {
		t.Fatalf("ResolveTelemetryConfig: %v", err)
	}
	if cfg.Enabled {
		t.Error("Enabled = true with CASCADE_TELEMETRY=0 and [telemetry] enabled=true, want false (never force-enable, hard-disable wins)")
	}
}

// TestTelemetryHardDisableNeverForceEnable covers both env paths this
// ticket's hard-disable AC names: CASCADE_TELEMETRY=0 force-disables
// regardless of the file, and the generic CASCADE_TELEMETRY__ENABLED
// override may narrow true->false but never widen false->true.
func TestTelemetryHardDisableNeverForceEnable(t *testing.T) {
	t.Run("named var force-disables over a true file value", testTelemetryNamedVarForceDisables)
	t.Run("generic override narrows true to false", func(t *testing.T) {
		dir := t.TempDir()
		path := writeConfigFile(t, dir, "[telemetry]\nenabled = true\n")
		tree, _, err := readAndUpgradeTree(path)
		if err != nil {
			t.Fatalf("readAndUpgradeTree: %v", err)
		}
		cfg, err := ResolveTelemetryConfig(tree, fakeEnviron(map[string]string{"CASCADE_TELEMETRY__ENABLED": "false"}), func(string) string { return "" }, nil)
		if err != nil {
			t.Fatalf("ResolveTelemetryConfig: %v", err)
		}
		if cfg.Enabled {
			t.Error("Enabled = true with CASCADE_TELEMETRY__ENABLED=false narrowing a true file value, want false")
		}
	})

	t.Run("generic override widen to true is refused", func(t *testing.T) {
		dir := t.TempDir()
		path := writeConfigFile(t, dir, "schema_version = 1\n")
		tree, _, err := readAndUpgradeTree(path)
		if err != nil {
			t.Fatalf("readAndUpgradeTree: %v", err)
		}
		cfg, err := ResolveTelemetryConfig(tree, fakeEnviron(map[string]string{"CASCADE_TELEMETRY__ENABLED": "true"}), func(string) string { return "" }, nil)
		if err != nil {
			t.Fatalf("ResolveTelemetryConfig: %v", err)
		}
		if cfg.Enabled {
			t.Error("Enabled = true via CASCADE_TELEMETRY__ENABLED=true with no file value, want false: no environment input may ever enable telemetry")
		}
	})
}

// TestTelemetryConfigRejectsInvalidValues covers the typed-error path: a
// non-boolean [telemetry] enabled value is a ConfigError naming
// telemetry.enabled, not a silent coercion or a panic.
func TestTelemetryConfigRejectsInvalidValues(t *testing.T) {
	dir := t.TempDir()
	path := writeConfigFile(t, dir, "[telemetry]\nenabled = \"yes\"\n")
	tree, _, err := readAndUpgradeTree(path)
	if err != nil {
		t.Fatalf("readAndUpgradeTree: %v", err)
	}

	_, err = ResolveTelemetryConfig(tree, fakeEnviron(nil), func(string) string { return "" }, nil)
	if err == nil {
		t.Fatal("ResolveTelemetryConfig: got nil error for a string [telemetry] enabled value, want a ConfigError")
	}
	cfgErr, ok := err.(*ConfigError)
	if !ok {
		t.Fatalf("error type = %T, want *ConfigError", err)
	}
	if cfgErr.Field != "telemetry.enabled" {
		t.Errorf("ConfigError.Field = %q, want %q", cfgErr.Field, "telemetry.enabled")
	}
}

// TestTelemetryConfig_UnknownKeyWarnsAndPreserves covers the
// warn-and-preserve path parseTelemetrySection shares with the other
// section parsers: an unrecognised key inside [telemetry] is not a hard
// error.
func TestTelemetryConfig_UnknownKeyWarnsAndPreserves(t *testing.T) {
	dir := t.TempDir()
	path := writeConfigFile(t, dir, "[telemetry]\nsample_rate = 1\n")
	tree, _, err := readAndUpgradeTree(path)
	if err != nil {
		t.Fatalf("readAndUpgradeTree: %v", err)
	}

	var warned []string
	warn := func(format string, _ ...interface{}) { warned = append(warned, format) }
	cfg, err := ResolveTelemetryConfig(tree, fakeEnviron(nil), func(string) string { return "" }, warn)
	if err != nil {
		t.Fatalf("ResolveTelemetryConfig: %v", err)
	}
	if cfg.Enabled {
		t.Error("Enabled = true with an unrelated unknown key present, want false (default unaffected)")
	}
	if len(warned) != 1 {
		t.Fatalf("warn calls = %d, want 1", len(warned))
	}
}
