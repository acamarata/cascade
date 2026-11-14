package docsguard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// ParityRow defines the schema for per-platform-sensitive capabilities.
type ParityRow struct {
	ID                  string            `yaml:"id"`
	Capability          string            `yaml:"capability"`
	Mechanism           map[string]string `yaml:"mechanism,omitempty"`
	UnsupportedPlatform string            `yaml:"unsupported_platform,omitempty"`
	Tests               []string          `yaml:"tests"`
}

// ParityRegistry is the top-level YAML document structure.
type ParityRegistry struct {
	Rows []ParityRow `yaml:"rows"`
}

var validPlatforms = map[string]bool{
	"linux": true, "windows": true, "darwin": true,
	"freebsd": true, "openbsd": true, "netbsd": true,
}

func loadParityRegistry(t *testing.T) ParityRegistry {
	t.Helper()
	path := filepath.Join("..", "build", "testdata", "platform_parity.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read platform_parity.yaml: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("platform_parity.yaml is empty")
	}
	var reg ParityRegistry
	if err := yaml.Unmarshal(data, &reg); err != nil {
		t.Fatalf("parse platform_parity.yaml: %v", err)
	}
	return reg
}

func validateParityRows(rows []ParityRow) error {
	seen := make(map[string]bool)
	for _, r := range rows {
		if r.ID == "" {
			return &parityError{msg: "empty id"}
		}
		if seen[r.ID] {
			return &parityError{msg: "duplicate id: " + r.ID}
		}
		seen[r.ID] = true
		if r.Capability == "" {
			return &parityError{msg: "empty capability for " + r.ID}
		}
		if len(r.Tests) == 0 {
			return &parityError{msg: "no tests for " + r.ID}
		}
		hasMech := len(r.Mechanism) > 0
		hasUnsup := r.UnsupportedPlatform != ""
		if !hasMech && !hasUnsup {
			return &parityError{msg: "empty mechanism for " + r.ID}
		}
		for k := range r.Mechanism {
			if !validPlatforms[k] {
				return &parityError{msg: "unknown platform key: " + k}
			}
		}
	}
	return nil
}

type parityError struct{ msg string }

func (e *parityError) Error() string { return e.msg }

// TestPlatformParityRegistryLoads parses all sixteen seeded rows without error.
func TestPlatformParityRegistryLoads(t *testing.T) {
	reg := loadParityRegistry(t)
	if len(reg.Rows) != 16 {
		t.Fatalf("got %d rows, want 16", len(reg.Rows))
	}
	if err := validateParityRows(reg.Rows); err != nil {
		t.Fatalf("validation failed: %v", err)
	}
	base := filepath.Join("..", "build", "testdata", "seeded-violations", "platform-parity")
	for _, r := range reg.Rows {
		file := filepath.Join(base, r.ID+".txt")
		info, err := os.Stat(file)
		if err != nil || info.IsDir() {
			t.Errorf("missing placeholder file for row %s", r.ID)
		}
	}
}

// TestPlatformParityRowSchemaRejects fails on unknown platform key, empty mechanism, or duplicate id.
func TestPlatformParityRowSchemaRejects(t *testing.T) {
	good := []ParityRow{{
		ID: "p1", Capability: "cap",
		Mechanism: map[string]string{"linux": "epoll"},
		Tests:     []string{"TestA"},
	}}
	if err := validateParityRows(good); err != nil {
		t.Fatalf("baseline rejected: %v", err)
	}

	badPlatform := []ParityRow{{
		ID: "p2", Capability: "cap",
		Mechanism: map[string]string{"plan9": "fs"},
		Tests:     []string{"TestB"},
	}}
	if err := validateParityRows(badPlatform); err == nil || !strings.Contains(err.Error(), "unknown platform") {
		t.Errorf("expected unknown platform rejection, got %v", err)
	}

	emptyMech := []ParityRow{{
		ID: "p3", Capability: "cap", Tests: []string{"TestC"},
	}}
	if err := validateParityRows(emptyMech); err == nil || !strings.Contains(err.Error(), "empty mechanism") {
		t.Errorf("expected empty mechanism rejection, got %v", err)
	}

	dupID := []ParityRow{
		{ID: "p4", Capability: "c1", UnsupportedPlatform: "refusal", Tests: []string{"T1"}},
		{ID: "p4", Capability: "c2", UnsupportedPlatform: "refusal", Tests: []string{"T2"}},
	}
	if err := validateParityRows(dupID); err == nil || !strings.Contains(err.Error(), "duplicate id") {
		t.Errorf("expected duplicate id rejection, got %v", err)
	}
}
