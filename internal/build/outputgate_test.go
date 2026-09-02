// Package build (this file) holds outputgate.go's real-tree and exemption
// proofs. Its seeded-violation counterparts live in the sibling
// outputgate_seeded_test.go, the same split boundary_seeded_test.go
// established for boundary_test.go (R-14.117).
package build

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// outputgateShippedRoots mirrors clockgateShippedRoots/stubgateShippedRoots:
// the shipped-code trees the live gate scans. Unlike the cmd/-only
// forbidigo rule it backstops, this list is NOT limited to cmd/ — R-14.137
// requires the whole-program property, which needs every non-exempt
// package scanned (see outputgate.go's package doc, "Cross-package").
var outputgateShippedRoots = []string{"cmd", "internal", "pkg", "providers", "plugins"}

// outputgateModuleRoot locates the repo root by walking up from this file.
func outputgateModuleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("output gate: runtime.Caller(0) failed")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("output gate: no go.mod found walking up from %s", file)
		}
		dir = parent
	}
}

// outputgateIsSkippedDir excludes testdata/ (fixtures) and dot dirs.
func outputgateIsSkippedDir(name string) bool {
	return name == "testdata" || (name != "." && strings.HasPrefix(name, "."))
}

// outputgateWalk walks root for non-test, non-exempt .go files and returns
// every denied stdout/stderr reference, denied bare print, or dot-import
// found.
func outputgateWalk(t *testing.T, root string, skipDirs bool) []OutputViolation {
	t.Helper()
	var out []OutputViolation
	if _, err := os.Stat(root); err != nil {
		return nil
	}
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipDirs && outputgateIsSkippedDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		if OutputgateIsExempt(path) {
			return nil
		}
		v, scanErr := OutputgateScanFile(path)
		if scanErr != nil {
			t.Fatalf("output gate: parsing %s: %v", path, scanErr)
		}
		out = append(out, v...)
		return nil
	})
	if walkErr != nil {
		t.Fatalf("output gate: walking %s: %v", root, walkErr)
	}
	return out
}

// TestOutputGate_RealTreeGreen: no denied os.Stdout/os.Stderr reference,
// denied bare print, or dot-import survives in the real tree outside the
// exempt internal/output and plugins/examples trees and test files.
func TestOutputGate_RealTreeGreen(t *testing.T) {
	root := outputgateModuleRoot(t)
	var all []OutputViolation
	for _, rel := range outputgateShippedRoots {
		all = append(all, outputgateWalk(t, filepath.Join(root, rel), true)...)
	}
	if len(all) != 0 {
		var b strings.Builder
		for _, v := range all {
			b.WriteString(v.File)
			b.WriteString(":")
			b.WriteString(v.Call)
			b.WriteString("\n")
		}
		t.Fatalf("output gate found %d violation(s) in the real tree:\n%s", len(all), b.String())
	}
}

// TestOutputGate_ExemptOutputPackageNotScanned proves internal/output.go,
// which legitimately names os.Stdout/os.Stderr in NewDefault, is exempt —
// a regression here would mean the gate is about to break the one
// sanctioned writer seam, not catch an evasion.
func TestOutputGate_ExemptOutputPackageNotScanned(t *testing.T) {
	root := outputgateModuleRoot(t)
	full := filepath.Join(root, "internal", "output", "output.go")
	if !OutputgateIsExempt(full) {
		t.Errorf("output gate: expected %s to be exempt, it is not", full)
	}
}

// TestOutputGate_ExemptExamplePluginNotScanned proves the standalone
// example plugin (D/S-06.T5's own carve-out) is exempt.
func TestOutputGate_ExemptExamplePluginNotScanned(t *testing.T) {
	root := outputgateModuleRoot(t)
	full := filepath.Join(root, "plugins", "examples", "example-builtin", "plugin.go")
	if !OutputgateIsExempt(full) {
		t.Errorf("output gate: expected %s to be exempt, it is not", full)
	}
}

// TestOutputGate_ExemptTestFilesNotScanned proves a _test.go file anywhere
// is exempt, matching the same allowance every other internal/build gate
// makes (Art.7.3).
func TestOutputGate_ExemptTestFilesNotScanned(t *testing.T) {
	if !OutputgateIsExempt(filepath.Join("internal", "runtime", "daemon_logs_test.go")) {
		t.Error("output gate: expected a _test.go path to be exempt, it is not")
	}
}

func outputgateFixtureDir(t *testing.T) string {
	t.Helper()
	return filepath.Join(outputgateModuleRoot(t), "internal", "build", "testdata", "seeded-violations", "outputgate")
}
