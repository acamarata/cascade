package main

// Purpose: TestHarvestRefusesLinkedOutputDir. An output dir, or any parent
// of one below the module root, that is a symlink is refused before a byte
// is written, so no fixture or README lands outside the root.
// Constraints: every path is under t.TempDir().

import (
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"testing"

	v1 "github.com/acamarata/cascade/internal/migration/v1"
)

// linkCase names the component of the memory output dir that becomes a link.
type linkCase struct {
	name  string
	level int // how many leading components of the memory output dir stay real
}

func TestHarvestRefusesLinkedOutputDir(t *testing.T) {
	parts := splitSlash(outputDirs[v1.DomainMemory])
	cases := []linkCase{{"the migration dir itself", len(parts) - 1}, {"a parent dir", 1}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := newModule(t)
			outside := t.TempDir()
			base := filepath.Join(append([]string{root}, parts[:tc.level]...)...)
			if err := os.MkdirAll(base, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(base, parts[tc.level])); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			code, _, stderr := runHarvest(t)
			if code != exitRefused {
				t.Fatalf("exit %d, want %d (stderr %q)", code, exitRefused, stderr)
			}
			if n := countFiles(t, outside); n != 0 {
				t.Errorf("%d files were written outside the module root", n)
			}
			if n := len(readOutputs(t, root)); n != 0 {
				t.Errorf("%d files were written under the module root after a refusal", n)
			}
		})
	}
}

// splitSlash splits a slash path into its components.
func splitSlash(p string) []string {
	var out []string
	for ; p != "." && p != "/" && p != ""; p = path.Dir(p) {
		out = append([]string{path.Base(p)}, out...)
	}
	return out
}

// countFiles counts every non-directory entry under dir.
func countFiles(t *testing.T, dir string) int {
	t.Helper()
	n := 0
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			n++
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}
