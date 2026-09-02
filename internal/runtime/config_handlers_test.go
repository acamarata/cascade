package runtime

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// Purpose: tests for the `cascade config` CLI-facing handlers in
//   config_handlers.go (ListEffectiveHandler, PathHandler) plus the
//   golden-fixture tests for env-precedence resolution and the
//   effective-config source annotation, both of which render through
//   those same handlers/EffectiveEntries. Split out of a single
//   config_test.go per R-14.117 (Art.10.3 file-cap remedy) —
//   behaviour-preserving, moved code only.
// Inputs: n/a (test-only).
// Outputs: n/a (test-only).
// Constraints: Art.11 — golden content must never contain timestamps,
//   absolute paths, or anything else non-deterministic.
// SPORT: runtime/config (ADD, placeholder per T-1 sport_updates).

func TestConfig_EffectiveEntriesAndHandlers(t *testing.T) {
	dir := t.TempDir()
	path := writeConfigFile(t, dir, "[runtime]\nprofile = \"server\"\n\n[logging]\nlevel = \"debug\"\n")
	cfg, err := Load(context.Background(), LoadOptions{
		Path:    path,
		Getenv:  func(string) string { return "" },
		Environ: fakeEnviron(nil),
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cfg.Runtime.Home = "/fake/root"
	cfg.Runtime.DataDir = "/fake/root/data"

	entries := cfg.EffectiveEntries()
	sawProfile := false
	sawLogging := false
	for _, e := range entries {
		if e.Key == "runtime.profile" {
			sawProfile = true
			if e.Value != "server" || e.Source != SourceFile {
				t.Errorf("runtime.profile entry = %+v", e)
			}
		}
		if e.Key == "logging.level" {
			sawLogging = true
		}
	}
	if !sawProfile || !sawLogging {
		t.Errorf("EffectiveEntries missing expected keys: %+v", entries)
	}

	human, err := ListEffectiveHandler(cfg, false)
	if err != nil || !strings.Contains(human, "runtime.profile = server (file)") {
		t.Errorf("ListEffectiveHandler human output = %q, err=%v", human, err)
	}
	jsonOut, err := ListEffectiveHandler(cfg, true)
	if err != nil || !strings.Contains(jsonOut, "\"runtime.profile\"") {
		t.Errorf("ListEffectiveHandler json output = %q, err=%v", jsonOut, err)
	}
}

func TestPathHandler(t *testing.T) {
	root := t.TempDir()
	p, err := NewPathProvider(func(k string) string {
		if k == "CASCADE_HOME" {
			return root
		}
		return ""
	}, fakeHomeDir(t))
	if err != nil {
		t.Fatalf("NewPathProvider: %v", err)
	}
	human, err := PathHandler(p, false)
	if err != nil || !strings.Contains(human, "root = "+root) {
		t.Errorf("PathHandler human output = %q, err=%v", human, err)
	}
	jsonOut, err := PathHandler(p, true)
	if err != nil || !strings.Contains(jsonOut, "\"socket\"") {
		t.Errorf("PathHandler json output = %q, err=%v", jsonOut, err)
	}
}

// TestConfigGoldenEnvPrecedence is the S-04.T1 golden-fixture test for
// env-precedence resolution (contract check: `go test ./internal/runtime/...
// -run TestConfigGolden -update`).
func TestConfigGoldenEnvPrecedence(t *testing.T) {
	dir := t.TempDir()
	path := writeConfigFile(t, dir, "[runtime]\nprofile = \"local\"\n\n[retrieval]\n[retrieval.fusion]\nk = 60\n")

	scenarios := []struct {
		name string
		flag string
		env  map[string]string
	}{
		{"default_from_file", "", nil},
		{"env_beats_file", "", map[string]string{"CASCADE_PROFILE": "server"}},
		{"flag_beats_env_and_file", "worker", map[string]string{"CASCADE_PROFILE": "server"}},
		{"generic_section_override", "", map[string]string{"CASCADE_RETRIEVAL__FUSION__K": "80"}},
	}

	var b strings.Builder
	for _, sc := range scenarios {
		cfg, err := Load(context.Background(), LoadOptions{
			Path:        path,
			ProfileFlag: sc.flag,
			Getenv: func(k string) string {
				return sc.env[k]
			},
			Environ: fakeEnviron(sc.env),
		})
		if err != nil {
			t.Fatalf("scenario %s: Load: %v", sc.name, err)
		}
		fmt.Fprintf(&b, "== %s ==\n", sc.name)
		fmt.Fprintf(&b, "runtime.profile = %s (%s)\n", cfg.Runtime.Profile, cfg.Source("runtime.profile"))
		if k, ok := cfg.Extra["retrieval"].(map[string]interface{}); ok {
			if fusion, ok := k["fusion"].(map[string]interface{}); ok {
				fmt.Fprintf(&b, "retrieval.fusion.k = %v (%s)\n", fusion["k"], cfg.Source("retrieval.fusion.k"))
			}
		}
	}
	compareGolden(t, "env_precedence.golden", b.String())
}

// TestConfigGoldenEffectiveAnnotation is the S-04.T1 golden-fixture test
// for the `cascade config list --effective` per-key source annotation.
func TestConfigGoldenEffectiveAnnotation(t *testing.T) {
	dir := t.TempDir()
	path := writeConfigFile(t, dir, "[runtime]\nprofile = \"server\"\n\n[elevation]\nallow_remote = false\n\n[logging]\nlevel = \"debug\"\n")
	cfg, err := Load(context.Background(), LoadOptions{
		Path:   path,
		Getenv: func(string) string { return "" },
		Environ: fakeEnviron(map[string]string{
			"CASCADE_LOGGING__FORMAT": "json",
		}),
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Home/DataDir are stamped by Bootstrap in production; fix them here
	// to a deterministic value so the golden never encodes a real path.
	cfg.Runtime.Home = "ROOT"
	cfg.Runtime.DataDir = "ROOT/data"

	compareGolden(t, "effective_config_annotation.golden", renderEffectiveGolden(cfg.EffectiveEntries()))
}
