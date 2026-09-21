// Purpose (this file): the D4 proof (confirming review finding 4) —
//
//	readTree refuses a symlink under the tree rather than following it.
//
// Constraints: windows: creating a symlink needs a privilege
//
//	(Developer Mode or admin) a CI runner may not have; a failed
//	os.Symlink is a named t.Skip, never a silent pass.
//
// SPORT: plugins/github/wiki:filetree (ADD) — P1-E25-W5-S51-T6.
package wiki

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// symlinkOrSkip creates a symlink at link pointing to target, skipping
// (with the reason) rather than failing when this platform/runner cannot
// create one — the standing windows rule: a named skip, never a silent
// pass.
func symlinkOrSkip(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink creation unavailable on this Windows runner: %v", err)
		}
		t.Fatalf("os.Symlink: %v", err)
	}
}

// TestReadTree_RefusesASymlinkUnderTheTree is the top-level case: a
// symlink pointing to a file OUTSIDE the tree must refuse the whole
// read, never be followed.
func TestReadTree_RefusesASymlinkUnderTheTree(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("outside content"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	symlinkOrSkip(t, outside, filepath.Join(dir, "Link.md"))

	_, err := readTree(dir)
	if err == nil {
		t.Fatal("readTree followed a symlink instead of refusing")
	}
	if !cascade.HasKind(err, cascade.KindPolicyDenied) {
		t.Fatalf("err kind = %v, want KindPolicyDenied", err)
	}
}

// TestReadTree_RefusesANestedSymlink proves the guard applies at any
// depth, not just the top level.
func TestReadTree_RefusesANestedSymlink(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	symlinkOrSkip(t, outside, filepath.Join(sub, "Link.md"))

	_, err := readTree(dir)
	if !cascade.HasKind(err, cascade.KindPolicyDenied) {
		t.Fatalf("err kind = %v, want KindPolicyDenied", err)
	}
}

// TestReadTree_NoSymlinksReadsNormally proves the guard adds no false
// positive: a tree with only regular files and directories reads exactly
// as before.
func TestReadTree_NoSymlinksReadsNormally(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Home.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "Nested.md"), []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	tree, err := readTree(dir)
	if err != nil {
		t.Fatalf("readTree: %v", err)
	}
	if string(tree["Home.md"]) != "x" || string(tree[filepath.Join("sub", "Nested.md")]) != "y" {
		t.Fatalf("tree = %v, missing expected regular files", tree)
	}
}

// TestReadTree_RefusesASymlinkedRoot covers depth 0: the tree root itself
// replaced by a symlink to an outside directory is refused, not followed.
func TestReadTree_RefusesASymlinkedRoot(t *testing.T) {
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "Home.md"), []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "wiki")
	symlinkOrSkip(t, outside, link)

	got, err := readTree(link)
	if err == nil {
		t.Fatalf("readTree followed a symlinked root and read %d file(s) from outside the tree", len(got))
	}
	if !cascade.HasKind(err, cascade.KindPolicyDenied) {
		t.Fatalf("err kind = %v, want KindPolicyDenied", err)
	}
}
