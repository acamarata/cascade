// Package build holds build-time gates on the repo tree itself. This file
// implements the no-desktop-only lint from P1-E01-W1-S01-T2: no file under
// internal/ may import a GUI-toolkit package, and no file under internal/
// may import a platform-only package outside a build-tagged file. Cascade's
// core is headless and product-agnostic (ASI Policy 2, PRI hard rule 1: "no
// UI in this repo, ever"). The gate is proven RED against a seeded fixture
// (materialized into t.TempDir(), Art.7.1) and GREEN on the real tree.
package build

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// desktopGUIPrefixes are GUI-toolkit import prefixes. There is no
// legitimate build-tagged exception for a GUI dependency in a headless
// core, so these are banned under internal/ unconditionally.
var desktopGUIPrefixes = []string{
	"fyne.io/",
	"gioui.org",
	"github.com/therecipe/qt",
	"github.com/lxn/walk",
	"github.com/gotk3/gotk3",
	"github.com/webview/webview",
	"github.com/progrium/macdriver",
	"github.com/andlabs/ui",
	"github.com/getlantern/systray",
	"github.com/sqweek/dialog",
}

// desktopPlatformOnlyPrefixes are legitimate platform-specific, non-GUI
// import prefixes (e.g. keychain/PAM bindings for the rule-14 elevation
// helper) allowed under internal/ only inside a build-tagged file.
var desktopPlatformOnlyPrefixes = []string{
	"golang.org/x/sys/windows",
	"golang.org/x/sys/unix",
	"syscall/js",
}

var desktopGOOS = []string{"darwin", "linux", "windows", "freebsd", "js", "wasm", "android", "ios"}

var desktopBuildTagLine = regexp.MustCompile(`^//\s*(go:build|\+build)\b`)

// desktopModuleRoot locates the repo root by walking up from this file.
func desktopModuleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("desktop-only lint: runtime.Caller(0) failed")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("desktop-only lint: no go.mod found walking up from %s", file)
		}
		dir = parent
	}
}

// desktopHasBuildConstraint reports whether path/src carries a GOOS build
// constraint: a GOOS filename suffix (optionally +GOARCH), or a leading
// //go:build (or legacy // +build) comment naming a desktopGOOS value.
func desktopHasBuildConstraint(path string, src []byte) bool {
	base := strings.TrimSuffix(filepath.Base(path), ".go")
	for _, part := range strings.Split(base, "_") {
		for _, goos := range desktopGOOS {
			if part == goos {
				return true
			}
		}
	}
	for _, line := range strings.Split(string(src), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "package ") {
			break
		}
		if !desktopBuildTagLine.MatchString(trimmed) {
			continue
		}
		for _, goos := range desktopGOOS {
			if strings.Contains(trimmed, goos) {
				return true
			}
		}
	}
	return false
}

// desktopMatchesAny reports whether p matches (equals or is prefixed by)
// any of prefixes.
func desktopMatchesAny(p string, prefixes []string) bool {
	for _, pre := range prefixes {
		if p == strings.TrimSuffix(pre, "/") || strings.HasPrefix(p, pre) {
			return true
		}
	}
	return false
}

// desktopIsSkippedDir excludes testdata/ (fixtures) and dot dirs.
func desktopIsSkippedDir(name string) bool {
	return name == "testdata" || (name != "." && strings.HasPrefix(name, "."))
}

// desktopScan walks root for non-test .go files and returns, sorted, every
// GUI-toolkit or unconstrained platform-only import violation found.
func desktopScan(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if desktopIsSkippedDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("desktop-only lint: reading %s: %v", path, err)
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, src, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("desktop-only lint: parsing %s: %v", path, err)
		}
		tagged := desktopHasBuildConstraint(path, src)
		for _, imp := range f.Imports {
			p := strings.Trim(imp.Path.Value, `"`)
			switch {
			case desktopMatchesAny(p, desktopGUIPrefixes):
				out = append(out, path+": GUI-toolkit import "+p+" (banned under internal/)")
			case !tagged && desktopMatchesAny(p, desktopPlatformOnlyPrefixes):
				out = append(out, path+": platform-only import "+p+" outside a build-tagged file")
			}
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("desktop-only lint: walking %s: %v", root, walkErr)
	}
	sort.Strings(out)
	return out
}

// desktopMaterialize copies the fixture tree at src into t.TempDir()
// (Art.7.1) and returns the copy's root.
func desktopMaterialize(t *testing.T, src string) string {
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
		t.Fatalf("desktop-only lint: materializing fixture %s: %v", src, walkErr)
	}
	return dst
}

// TestNoDesktopOnlyImports_RealTreeGreen: no desktop-only import exists
// under the real internal/ tree.
func TestNoDesktopOnlyImports_RealTreeGreen(t *testing.T) {
	root := desktopModuleRoot(t)
	v := desktopScan(t, filepath.Join(root, "internal"))
	if len(v) != 0 {
		t.Fatalf("desktop-only import(s) found under internal/:\n%s", strings.Join(v, "\n"))
	}
}

// TestNoDesktopOnlyImports_SeededViolationRed: the seeded fixture's
// unconstrained GUI import trips the lint.
func TestNoDesktopOnlyImports_SeededViolationRed(t *testing.T) {
	root := desktopModuleRoot(t)
	src := filepath.Join(root, "internal", "build", "testdata", "seeded-violations", "desktop-only")
	fixture := desktopMaterialize(t, src)
	v := desktopScan(t, filepath.Join(fixture, "internal"))
	if len(v) == 0 {
		t.Fatal("expected a desktop-only import violation in seeded fixture, found none")
	}
}
