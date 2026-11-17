package mod

import (
	"errors"
	"testing"
	"testing/fstest"
)

func TestLoadRulesReadsFile(t *testing.T) {
	fsys := fstest.MapFS{
		"wp-protocol.md": &fstest.MapFile{Data: []byte("rule A")},
	}
	got, err := LoadRules(fsys, []string{"wp-protocol.md"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got["wp-protocol.md"] != "rule A" {
		t.Fatalf("expected rule A, got %q", got["wp-protocol.md"])
	}
}

func TestLoadRulesMissingFails(t *testing.T) {
	fsys := fstest.MapFS{}
	_, err := LoadRules(fsys, []string{"missing.md"})
	if err == nil {
		t.Fatal("expected error")
	}
	var mErr ErrRuleMissing
	if !errors.As(err, &mErr) {
		t.Fatalf("expected ErrRuleMissing, got %v", err)
	}
	if string(mErr) != "missing.md" {
		t.Fatalf("expected missing.md, got %s", mErr)
	}
}

func TestLoadRulesEmptyFails(t *testing.T) {
	fsys := fstest.MapFS{
		"empty.md": &fstest.MapFile{Data: []byte("")},
	}
	_, err := LoadRules(fsys, []string{"empty.md"})
	if !errors.Is(err, ErrRuleEmpty) {
		t.Fatalf("expected ErrRuleEmpty, got %v", err)
	}
}

func TestLoadRulesRejectsTraversal(t *testing.T) {
	fsys := fstest.MapFS{}
	_, err := LoadRules(fsys, []string{"../secret.md"})
	if !errors.Is(err, ErrRuleName) {
		t.Fatalf("expected ErrRuleName, got %v", err)
	}
}
