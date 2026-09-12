//go:build windows

// Purpose: proves, natively in the windows CI lane, that this package's
//
//	one path-shaped surface — LoadProjectList's relative-path resolution
//	and MergeProjectPaths' dedup — behaves correctly under windows'
//	backslash separator and drive-letter absolute paths. Everything else
//	in this package is pure file I/O with no OS-specific behavior
//	(Article-5): darwin/linux CI already exercises the rest of this
//	package identically, so this file stays narrow rather than
//	duplicating the whole suite under a build tag.
//
// SPORT: internal/migration [ADD] (P1-E26-W10-S53-T3 sport_updates).
package migration

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadProjectListWindowsPaths asserts a relative line resolves against
// base using filepath.Join (backslash-joined on windows), and that a
// drive-letter absolute line (`C:\...`) is left as-is rather than
// re-joined against base.
func TestLoadProjectListWindowsPaths(t *testing.T) {
	dir := t.TempDir()
	listPath := filepath.Join(dir, "projects.txt")
	content := "relative-project\r\nC:\\Users\\op\\other-project\r\n"
	if err := os.WriteFile(listPath, []byte(content), 0o600); err != nil {
		t.Fatalf("write list: %v", err)
	}
	got, err := LoadProjectList(listPath, dir)
	if err != nil {
		t.Fatalf("LoadProjectList: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("LoadProjectList = %v, want 2 entries", got)
	}
	wantRel := filepath.Join(dir, "relative-project")
	if got[0] != wantRel {
		t.Fatalf("relative entry = %q, want %q", got[0], wantRel)
	}
	wantAbs := `C:\Users\op\other-project`
	if got[1] != wantAbs {
		t.Fatalf("absolute entry = %q, want %q (must not be rejoined against base)", got[1], wantAbs)
	}
}

// TestMergeProjectPathsWindowsDedup asserts two spellings of the same
// windows path (with and without a trailing separator, which
// filepath.Clean normalizes) dedupe to one entry, exactly as the
// darwin/linux behavior already does.
func TestMergeProjectPathsWindowsDedup(t *testing.T) {
	got := MergeProjectPaths(
		[]string{`C:\Sites\proj\`},
		[]string{`C:\Sites\proj`},
	)
	if len(got) != 1 {
		t.Fatalf("MergeProjectPaths = %v, want exactly 1 deduped entry", got)
	}
}
