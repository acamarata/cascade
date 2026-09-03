package runtime

// Purpose: tests for DecodeConfigFile (config_write_secrets.go) — the
//   read-and-decode helper behind `cascade config validate`/`edit`, which
//   was previously exercised only indirectly (0% direct coverage). Kept
//   as its own file rather than growing config_write_secrets_scan_test.go
//   past a size that would blur its own single-purpose scope.
// Inputs: n/a (test-only).
// Outputs: n/a (test-only).
// Constraints: Art.7.1 — every path used is under t.TempDir().
// SPORT: runtime/config-write-verbs (ADD, placeholder per T-8 sport_updates).

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDecodeConfigFile_MissingFileReturnsEmptyTree(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "does-not-exist.toml")

	tree, err := DecodeConfigFile(path)
	if err != nil {
		t.Fatalf("DecodeConfigFile on a missing file: %v", err)
	}
	if len(tree) != 0 {
		t.Fatalf("tree = %+v, want empty map for a not-yet-existing file", tree)
	}
}

func TestDecodeConfigFile_ValidTOML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	const body = "[logging]\nlevel = \"debug\"\nformat = \"json\"\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("seed config file: %v", err)
	}

	tree, err := DecodeConfigFile(path)
	if err != nil {
		t.Fatalf("DecodeConfigFile: %v", err)
	}
	logging, ok := tree["logging"].(map[string]interface{})
	if !ok {
		t.Fatalf("tree[logging] = %+v (%T), want map[string]interface{}", tree["logging"], tree["logging"])
	}
	if logging["level"] != "debug" || logging["format"] != "json" {
		t.Fatalf("logging = %+v, want level=debug format=json", logging)
	}
}

func TestDecodeConfigFile_EmptyFileReturnsEmptyTree(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.toml")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("seed empty config file: %v", err)
	}

	tree, err := DecodeConfigFile(path)
	if err != nil {
		t.Fatalf("DecodeConfigFile on an empty file: %v", err)
	}
	if len(tree) != 0 {
		t.Fatalf("tree = %+v, want empty map for an empty file", tree)
	}
}

func TestDecodeConfigFile_MalformedTOMLReturnsConfigError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.toml")
	if err := os.WriteFile(path, []byte("not valid toml {"), 0o644); err != nil {
		t.Fatalf("seed malformed config file: %v", err)
	}

	_, err := DecodeConfigFile(path)
	if err == nil {
		t.Fatal("DecodeConfigFile on malformed TOML: want error, got nil")
	}
	var ce *ConfigError
	if !errors.As(err, &ce) {
		t.Fatalf("error = %v (%T), want *ConfigError", err, err)
	}
}
