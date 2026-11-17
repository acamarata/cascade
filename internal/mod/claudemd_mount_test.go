package mod

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMountClaudeMDIdenticalContent(t *testing.T) {
	d1 := t.TempDir()
	d2 := t.TempDir()

	src := filepath.Join(t.TempDir(), "CLAUDE.md")
	content := []byte("RULES")
	if err := os.WriteFile(src, content, 0644); err != nil {
		t.Fatal(err)
	}

	err := MountClaudeMD(src, []string{d1, d2})
	if err != nil {
		t.Fatalf("MountClaudeMD failed: %v", err)
	}

	got1, err := os.ReadFile(filepath.Join(d1, "CLAUDE.md"))
	if err != nil {
		t.Fatalf("ReadFile d1 failed: %v", err)
	}
	if string(got1) != "RULES" {
		t.Errorf("d1 content = %q, want %q", string(got1), "RULES")
	}

	got2, err := os.ReadFile(filepath.Join(d2, "CLAUDE.md"))
	if err != nil {
		t.Fatalf("ReadFile d2 failed: %v", err)
	}
	if string(got2) != "RULES" {
		t.Errorf("d2 content = %q, want %q", string(got2), "RULES")
	}
}

func TestMountClaudeMDMissingSource(t *testing.T) {
	d1 := t.TempDir()
	err := MountClaudeMD(filepath.Join(t.TempDir(), "nonexistent"), []string{d1})
	if !errors.Is(err, ErrMissingSource) {
		t.Errorf("err = %v, want ErrMissingSource", err)
	}
}

func TestMountClaudeMDMissingThreadDir(t *testing.T) {
	src := filepath.Join(t.TempDir(), "CLAUDE.md")
	if err := os.WriteFile(src, []byte("RULES"), 0644); err != nil {
		t.Fatal(err)
	}

	nonexistentDir := filepath.Join(t.TempDir(), "does_not_exist")
	err := MountClaudeMD(src, []string{nonexistentDir})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrMountFailed) {
		t.Errorf("err = %v, want ErrMountFailed", err)
	}
	if !strings.Contains(err.Error(), nonexistentDir) {
		t.Errorf("err = %v, want it to contain dir %q", err, nonexistentDir)
	}
}

func TestMountClaudeMDOverwritesStale(t *testing.T) {
	d1 := t.TempDir()
	staleFile := filepath.Join(d1, "CLAUDE.md")
	if err := os.WriteFile(staleFile, []byte("OLD"), 0644); err != nil {
		t.Fatal(err)
	}

	src := filepath.Join(t.TempDir(), "CLAUDE.md")
	if err := os.WriteFile(src, []byte("RULES"), 0644); err != nil {
		t.Fatal(err)
	}

	err := MountClaudeMD(src, []string{d1})
	if err != nil {
		t.Fatalf("MountClaudeMD failed: %v", err)
	}

	got, err := os.ReadFile(staleFile)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if string(got) != "RULES" {
		t.Errorf("content = %q, want %q", string(got), "RULES")
	}
}
