package mod

import (
	"os"
	"path/filepath"
	"testing"
)

func TestModuleNamesMatchModulesMD(t *testing.T) {
	dir := t.TempDir()
	mdPath := filepath.Join(dir, "MODULES.md")
	mdContent := "## Crunch\nSome description.\n"
	if err := os.WriteFile(mdPath, []byte(mdContent), 0644); err != nil {
		t.Fatal(err)
	}
	lane := map[string]string{
		"step1": "run mod:Crunch",
	}
	violations, err := CheckModuleNames(mdPath, lane)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(violations) != 0 {
		t.Fatalf("expected 0 violations, got %d: %v", len(violations), violations)
	}
}

func TestUndefinedModuleNameFails(t *testing.T) {
	dir := t.TempDir()
	mdPath := filepath.Join(dir, "MODULES.md")
	mdContent := "## Crunch\nSome description.\n"
	if err := os.WriteFile(mdPath, []byte(mdContent), 0644); err != nil {
		t.Fatal(err)
	}
	lane := map[string]string{
		"step1": "run mod:Foo",
	}
	violations, err := CheckModuleNames(mdPath, lane)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(violations) != 1 {
		t.Fatalf("expected 1 violation, got %d: %v", len(violations), violations)
	}
	if violations[0] != "Foo" {
		t.Fatalf("expected violation 'Foo', got %q", violations[0])
	}
}

func TestModuleNameCaseMismatchFails(t *testing.T) {
	dir := t.TempDir()
	mdPath := filepath.Join(dir, "MODULES.md")
	mdContent := "## Crunch\nSome description.\n"
	if err := os.WriteFile(mdPath, []byte(mdContent), 0644); err != nil {
		t.Fatal(err)
	}
	lane := map[string]string{
		"step1": "run mod:crunch",
	}
	violations, err := CheckModuleNames(mdPath, lane)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(violations) != 1 {
		t.Fatalf("expected 1 violation, got %d: %v", len(violations), violations)
	}
	if violations[0] != "crunch" {
		t.Fatalf("expected violation 'crunch', got %q", violations[0])
	}
}

func TestMissingModulesMDFailsClosed(t *testing.T) {
	dir := t.TempDir()
	mdPath := filepath.Join(dir, "MODULES.md")
	lane := map[string]string{
		"step1": "run mod:Crunch",
	}
	_, err := CheckModuleNames(mdPath, lane)
	if err == nil {
		t.Fatal("expected error for missing MODULES.md, got nil")
	}
}
