// Package build holds build-time gates on the repo tree itself. This file
// implements the 300-line file-cap gate from P1-E01-W1-S01-T2
// (12-QUALITY-CONSTITUTION.md Art.10.3): no tracked .go file may exceed 300
// lines. testdata/ fixtures are excluded — they are data, never shipped
// code (Art.1/Art.7.1). The gate is proven RED against a seeded oversized
// fixture (materialized into t.TempDir(), Art.7.1) and GREEN on the real
// tree.
package build

import (
	"bufio"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// filecapMaxLines is the Art.10.3 cap.
const filecapMaxLines = 300

// filecapModuleRoot locates the repo root by walking up from this file.
func filecapModuleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("file cap gate: runtime.Caller(0) failed")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("file cap gate: no go.mod found walking up from %s", file)
		}
		dir = parent
	}
}

// filecapCountLines counts newline-terminated lines plus one for a final
// unterminated line (matches `wc -l`-style counting for text files).
func filecapCountLines(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	n := 0
	for scanner.Scan() {
		n++
	}
	return n, scanner.Err()
}

// filecapIsSkippedDir excludes testdata/ (fixtures) and dot dirs.
func filecapIsSkippedDir(name string) bool {
	return name == "testdata" || (name != "." && strings.HasPrefix(name, "."))
}

// filecapScan walks root for .go files and returns, sorted, a description
// of every one over filecapMaxLines lines.
func filecapScan(t *testing.T, root string) []string {
	t.Helper()
	var violations []string
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if filecapIsSkippedDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		n, err := filecapCountLines(path)
		if err != nil {
			t.Fatalf("file cap gate: reading %s: %v", path, err)
		}
		if n > filecapMaxLines {
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				rel = path
			}
			violations = append(violations, rel+" ("+strconv.Itoa(n)+" lines)")
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("file cap gate: walking %s: %v", root, walkErr)
	}
	sort.Strings(violations)
	return violations
}

// filecapMaterialize copies the fixture tree at src into t.TempDir()
// (Art.7.1) and returns the copy's root.
func filecapMaterialize(t *testing.T, src string) string {
	t.Helper()
	dst := t.TempDir()
	walkErr := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if walkErr != nil {
		t.Fatalf("file cap gate: materializing fixture %s: %v", src, walkErr)
	}
	return dst
}

// TestFileCap300Lines_RealTreeGreen: no .go file in the real tree exceeds
// the cap.
func TestFileCap300Lines_RealTreeGreen(t *testing.T) {
	root := filecapModuleRoot(t)
	v := filecapScan(t, root)
	if len(v) != 0 {
		t.Fatalf("file(s) over %d lines in real tree:\n%s", filecapMaxLines, strings.Join(v, "\n"))
	}
}

// TestFileCap300Lines_SeededViolationRed: the oversized fixture trips the cap.
func TestFileCap300Lines_SeededViolationRed(t *testing.T) {
	root := filecapModuleRoot(t)
	src := filepath.Join(root, "internal", "build", "testdata", "seeded-violations", "filecap")
	fixture := filecapMaterialize(t, src)
	v := filecapScan(t, fixture)
	if len(v) == 0 {
		t.Fatal("expected an over-300-line file in seeded fixture, found none")
	}
}
