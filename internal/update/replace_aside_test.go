package update

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestReplaceAsideRenameBackFailureIsReported: when moving the new file into
// place fails and renaming <name>.old back fails too, dst is gone, so the
// error must carry both causes and the install.sh remedy; it must never be
// the bare first error.
func TestReplaceAsideRenameBackFailureIsReported(t *testing.T) {
	dir := t.TempDir()
	dst, src := filepath.Join(dir, "cascade"), filepath.Join(dir, ".cascade.new")
	must(t, os.WriteFile(dst, []byte("old"), 0o755))
	must(t, os.WriteFile(src, []byte("new"), 0o755))
	rename := func(a, b string) error {
		if b == dst { // moving into place and renaming back both fail
			return errors.New("injected rename failure " + filepath.Base(a))
		}
		return os.Rename(a, b)
	}
	err := replaceByRenameAside(rename, src, dst)
	if err == nil {
		t.Fatal("replace succeeded with both renames failing")
	}
	for _, want := range []string{"injected rename failure .cascade.new", "injected rename failure cascade.old", "rerun install.sh"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q lacks %q", err, want)
		}
	}
	if !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("error %v is not KindIntegrity", err)
	}
	if _, serr := os.Lstat(dst + ".old"); serr != nil {
		t.Fatalf("the set-aside image is gone: %v", serr)
	}
}

// TestReplaceAsideRollsBackWhenMoveFails: the rename-back succeeds, dst is
// intact and the error is the move's own, with no remedy text.
func TestReplaceAsideRollsBackWhenMoveFails(t *testing.T) {
	dir := t.TempDir()
	dst, src := filepath.Join(dir, "cascade"), filepath.Join(dir, ".cascade.new")
	must(t, os.WriteFile(dst, []byte("old"), 0o755))
	must(t, os.WriteFile(src, []byte("new"), 0o755))
	rename := func(a, b string) error {
		if a == src {
			return errors.New("injected move failure")
		}
		return os.Rename(a, b)
	}
	err := replaceByRenameAside(rename, src, dst)
	if err == nil || !strings.Contains(err.Error(), "injected move failure") || strings.Contains(err.Error(), "install.sh") {
		t.Fatalf("err = %v", err)
	}
	if b, rerr := os.ReadFile(dst); rerr != nil || string(b) != "old" {
		t.Fatalf("dst after rollback = %q, %v", b, rerr)
	}
}

// TestReplaceAsideSuccessAndFreshTarget covers the success path with and
// without an existing dst.
func TestReplaceAsideSuccessAndFreshTarget(t *testing.T) {
	for _, existing := range []bool{true, false} {
		dir := t.TempDir()
		dst, src := filepath.Join(dir, "cascade"), filepath.Join(dir, ".cascade.new")
		if existing {
			must(t, os.WriteFile(dst, []byte("old"), 0o755))
		}
		must(t, os.WriteFile(src, []byte("new"), 0o755))
		must(t, replaceByRenameAside(os.Rename, src, dst))
		if b, err := os.ReadFile(dst); err != nil || string(b) != "new" {
			t.Fatalf("existing=%v dst = %q, %v", existing, b, err)
		}
	}
}
