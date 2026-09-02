package runtime

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Purpose: shared test fixtures for every config_*_test.go file in this
//   package (golden-fixture comparison, deterministic tree rendering,
//   fake environment/file builders) plus the tests for the core types in
//   config.go (ConfigError, Config.Source nil-safety). Split out of a
//   single config_test.go per R-14.117 (Art.10.3 file-cap remedy) —
//   behaviour-preserving, moved code only; every helper here is reused by
//   config_load_test.go, config_env_test.go, and config_handlers_test.go
//   (same package, no re-declaration).
// Inputs: n/a (test-only).
// Outputs: n/a (test-only).
// Constraints: Art.11 — golden content must never contain timestamps,
//   absolute paths, or anything else non-deterministic.
// SPORT: runtime/config (ADD, placeholder per T-1 sport_updates).

// updateGolden is the shared -update flag for every golden-fixture test in
// this package (config_handlers_test.go, schema_test.go). Contract check:
// `go test ./internal/runtime/... -run TestConfigGolden -update`.
var updateGolden = flag.Bool("update", false, "update golden fixture files")

// compareGolden compares actual against testdata/goldens/<name>, or
// rewrites the fixture when -update is passed. Golden content must never
// contain timestamps, absolute paths, or anything else non-deterministic
// (Art.11: a flaky gate is forbidden).
func compareGolden(t *testing.T, name, actual string) {
	t.Helper()
	path := filepath.Join("testdata", "goldens", name)
	if *updateGolden {
		if err := os.WriteFile(path, []byte(actual), 0o644); err != nil {
			t.Fatalf("write golden %s: %v", path, err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v (run with -update to create it)", path, err)
	}
	if actual != string(want) {
		t.Errorf("golden mismatch for %s:\n--- want ---\n%s\n--- got ---\n%s", name, want, actual)
	}
}

// renderSortedKV renders m as deterministic, sorted "key = value" lines,
// dotted-path style. Used by every golden test in this package so fixture
// content never depends on Go map iteration order.
func renderSortedKV(m map[string]interface{}) string {
	flat := map[string]interface{}{}
	flattenTree(m, "", flat)
	keys := make([]string, 0, len(flat))
	for k := range flat {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%s = %v\n", k, flat[k])
	}
	return b.String()
}

func renderSchemaUpgradeGolden(result SchemaUpgradeResult, tree map[string]interface{}) string {
	var b strings.Builder
	fmt.Fprintf(&b, "from=%d to=%d mutated=%v\n", result.FromVersion, result.ToVersion, result.Mutated)
	b.WriteString(renderSortedKV(tree))
	return b.String()
}

func renderEffectiveGolden(entries []EffectiveEntry) string {
	var b strings.Builder
	for _, e := range entries {
		fmt.Fprintf(&b, "%s = %v (%s)\n", e.Key, e.Value, e.Source)
	}
	return b.String()
}

// fakeEnviron builds an os.Environ()-shaped []string from a map, sorted
// for deterministic iteration in tests.
func fakeEnviron(m map[string]string) func() []string {
	return func() []string {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := make([]string, 0, len(keys))
		for _, k := range keys {
			out = append(out, k+"="+m[k])
		}
		return out
	}
}

func writeConfigFile(t *testing.T, dir, contents string) string {
	t.Helper()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}
	return path
}

func TestConfigError_Error(t *testing.T) {
	withField := &ConfigError{Field: "elevation.helper_pubkey", Reason: "required"}
	if !strings.Contains(withField.Error(), "elevation.helper_pubkey") {
		t.Errorf("Error() = %q, want it to name the field", withField.Error())
	}
	withoutField := &ConfigError{Reason: "malformed TOML"}
	if !strings.Contains(withoutField.Error(), "malformed TOML") {
		t.Errorf("Error() = %q, want it to include the reason", withoutField.Error())
	}
}

func TestConfig_Source_NilSafety(t *testing.T) {
	var cfg *Config
	if cfg.Source("anything") != SourceDefault {
		t.Error("nil *Config.Source() should report SourceDefault, not panic")
	}
	empty := &Config{}
	if empty.Source("anything") != SourceDefault {
		t.Error("Config with nil sources map should report SourceDefault")
	}
}
