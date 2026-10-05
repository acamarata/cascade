package main

// Purpose: input pinning. Every refusal exits 2, names the relative path,
// and leaves every output dir empty.

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// assertRefusedNothingWritten runs the harvester and checks the refusal.
func assertRefusedNothingWritten(t *testing.T, root, wantStderr string) {
	t.Helper()
	code, _, stderr := runHarvest(t)
	if code != exitRefused {
		t.Fatalf("exit %d, want %d (stderr %q)", code, exitRefused, stderr)
	}
	if !strings.Contains(stderr, wantStderr) {
		t.Errorf("stderr %q does not name %q", stderr, wantStderr)
	}
	if got := readOutputs(t, root); len(got) != 0 {
		t.Fatalf("%d files written after a refusal", len(got))
	}
}

func TestHarvestRefusesUncommittedSource(t *testing.T) {
	root := newModule(t)
	writeTestFile(t, filepath.Join(pinnedOf(root), "memory", "untracked.md"), []byte("# Untracked\n"))
	assertRefusedNothingWritten(t, root, "memory/untracked.md is not listed in INPUTS.sha256")
}

func TestHarvestRefusesModifiedInput(t *testing.T) {
	root := newModule(t)
	path := filepath.Join(pinnedOf(root), "accounts", "accounts.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, path, append(data, ' '))
	assertRefusedNothingWritten(t, root, "accounts/accounts.json does not match its INPUTS.sha256 digest")
}

func TestHarvestRefusesMissingManifest(t *testing.T) {
	root := newModule(t)
	if err := os.Remove(filepath.Join(pinnedOf(root), manifestName)); err != nil {
		t.Fatal(err)
	}
	assertRefusedNothingWritten(t, root, "INPUTS.sha256 is missing")
}

func TestHarvestRejectsUnsafeInput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks needs a privilege Windows CI does not grant")
	}
	t.Run("symlink", func(t *testing.T) {
		root := newModule(t)
		target := filepath.Join(t.TempDir(), "outside.md")
		writeTestFile(t, target, []byte("# Outside\n"))
		if err := os.Symlink(target, filepath.Join(pinnedOf(root), "memory", "linked.md")); err != nil {
			t.Fatal(err)
		}
		assertRefusedNothingWritten(t, root, "memory/linked.md is a symlink")
	})
	t.Run("dotdot", func(t *testing.T) {
		root := newModule(t)
		manifest := filepath.Join(pinnedOf(root), manifestName)
		data, err := os.ReadFile(manifest)
		if err != nil {
			t.Fatal(err)
		}
		climbing := strings.Replace(string(data), "  memory/decisions.md", "  memory/../../decisions.md", 1)
		writeTestFile(t, manifest, []byte(climbing))
		assertRefusedNothingWritten(t, root, "climbs out of the pinned dir")
	})
	t.Run("root-outside-pinned-dir", func(t *testing.T) {
		root := newModule(t)
		pinned := pinnedOf(root)
		elsewhere := filepath.Join(t.TempDir(), "elsewhere")
		if err := os.Rename(pinned, elsewhere); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(elsewhere, pinned); err != nil {
			t.Fatal(err)
		}
		assertRefusedNothingWritten(t, root, "the pinned input dir is missing or is not a real directory")
	})
}

func TestParseManifest_Refusals(t *testing.T) {
	sum := strings.Repeat("a", 64)
	cases := map[string]string{
		"empty":        "",
		"no-newline":   sum + "  memory/a.md",
		"crlf":         sum + "  memory/a.md\r\n",
		"short-digest": "abc  memory/a.md\n",
		"upper-hex":    strings.Repeat("A", 64) + "  memory/a.md\n",
		"one-space":    sum + " memory/a.md\n",
		"absolute":     sum + "  /memory/a.md\n",
		"backslash":    sum + "  memory\\a.md\n",
		"unclean":      sum + "  memory//a.md\n",
		"other-dir":    sum + "  recall/a.md\n",
		"top-level":    sum + "  memory\n",
		"readme":       sum + "  memory/README.md\n",
		"unsorted":     sum + "  vault/b.env\n" + sum + "  memory/a.md\n",
		"duplicate":    sum + "  memory/a.md\n" + sum + "  memory/a.md\n",
	}
	for name, text := range cases {
		if _, err := parseManifest(text); err == nil {
			t.Errorf("%s: manifest accepted", name)
		}
	}
	got, err := parseManifest(sum + "  memory/a.md\n" + sum + "  vault/b.env\n")
	if err != nil || len(got) != 2 || got[1].rel != "vault/b.env" {
		t.Fatalf("valid manifest = %v, %v", got, err)
	}
}

func TestLoadPinned_ListedFileMissing(t *testing.T) {
	root := newModule(t)
	if err := os.Remove(filepath.Join(pinnedOf(root), "vault", "vault.env")); err != nil {
		t.Fatal(err)
	}
	if _, err := loadPinned(pinnedOf(root)); err == nil || !strings.Contains(err.Error(), "vault/vault.env is listed but missing") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadPinned_CommittedSetVerifies(t *testing.T) {
	files, err := loadPinned(realPinnedDir)
	if err != nil {
		t.Fatalf("the committed input set does not verify: %v", err)
	}
	if len(files) != 8 {
		t.Errorf("committed input set has %d files, want 8", len(files))
	}
}

func TestWithin(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "a", "b")
	if !within(root, filepath.Join(root, "c")) || within(root, filepath.Join(root, "..", "x")) {
		t.Error("within misclassified a path")
	}
}
