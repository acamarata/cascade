package sync

import (
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestParseSectionAbsentIsEmptyConfig(t *testing.T) {
	cfg, err := ParseSection(map[string]interface{}{}, nil)
	if err != nil {
		t.Fatalf("ParseSection: %v", err)
	}
	if len(cfg.Overrides) != 0 {
		t.Fatalf("Overrides = %v, want empty", cfg.Overrides)
	}
}

func TestParseSectionValidOverride(t *testing.T) {
	tree := map[string]interface{}{"sync": map[string]interface{}{"config/config": "local-only"}}
	cfg, err := ParseSection(tree, nil)
	if err != nil {
		t.Fatalf("ParseSection: %v", err)
	}
	if cfg.Overrides["config/config"] != ClassLocalOnly {
		t.Fatalf("override = %q, want local-only", cfg.Overrides["config/config"])
	}
}

func TestParseSectionRejectsUnknownClass(t *testing.T) {
	tree := map[string]interface{}{"sync": map[string]interface{}{"config/config": "bogus-class"}}
	if _, err := ParseSection(tree, nil); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("unknown class: want KindInvalidInput, got %v", err)
	}
}

func TestParseSectionRejectsNonTable(t *testing.T) {
	tree := map[string]interface{}{"sync": "not-a-table"}
	if _, err := ParseSection(tree, nil); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("non-table section: want KindInvalidInput, got %v", err)
	}
}

func TestParseSectionHotReloadNarrowingAllowed(t *testing.T) {
	prev := Config{Overrides: map[string]Class{"memory/memory": ClassSynced}}
	tree := map[string]interface{}{"sync": map[string]interface{}{"memory/memory": "local-only"}}
	if _, err := ParseSection(tree, &prev); err != nil {
		t.Fatalf("narrowing reload must be allowed: %v", err)
	}
}

func TestParseSectionHotReloadWideningRefused(t *testing.T) {
	prev := Config{Overrides: map[string]Class{"memory/memory": ClassLocalOnly}}
	tree := map[string]interface{}{"sync": map[string]interface{}{"memory/memory": "synced"}}
	_, err := ParseSection(tree, &prev)
	if !cascade.HasKind(err, cascade.KindPolicyDenied) {
		t.Fatalf("widening reload: want KindPolicyDenied, got %v", err)
	}
}

func TestConfigResolveFallsBackToCompiled(t *testing.T) {
	cfg := Config{Overrides: map[string]Class{}}
	if got := cfg.Resolve("memory/memory", ClassSynced); got != ClassSynced {
		t.Fatalf("Resolve fallback = %q, want synced", got)
	}
	cfg.Overrides["memory/memory"] = ClassLocalOnly
	if got := cfg.Resolve("memory/memory", ClassSynced); got != ClassLocalOnly {
		t.Fatalf("Resolve override = %q, want local-only", got)
	}
}
