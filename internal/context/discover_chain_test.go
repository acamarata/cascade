package context

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
)

// Exercises the discovery hardening: the below-root PAC chain, the
// `..cache` ancestor test, the bounded and link-safe tier read, and git
// failure classification. Real filesystems and a real git binary throughout;
// the only stand-in is a stub `git` on PATH where a failure mode cannot be
// produced by the real one.

// plantTier plants <dir>/.cascade/CASCADE.md with content.
func plantTier(t *testing.T, dir, content string) {
	t.Helper()
	td := filepath.Join(dir, tierDirName)
	if err := os.MkdirAll(td, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(td, tierFileName), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func rolesOf(records []TierRecord) []TierRole {
	out := make([]TierRole, len(records))
	for i, r := range records {
		out[i] = r.Role
	}
	return out
}

func TestDiscoverWalksFullChain(t *testing.T) {
	root := resolvedTempDir(t)
	repo := filepath.Join(root, "repo")
	cwd := filepath.Join(repo, "a", "b", "c")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "init", "-q")
	plantTier(t, filepath.Join(repo, "a"), "mid")
	plantTier(t, cwd, "leaf")

	records, err := Discover(context.Background(), cwd, fixedHome(filepath.Join(root, "home")))
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	want := []TierRole{TierGCI, TierAPC, TierPPC, TierPRC, TierPAC, TierPAC}
	if !slices.Equal(rolesOf(records), want) {
		t.Fatalf("roles = %v, want %v", rolesOf(records), want)
	}
	for i, r := range records {
		if r.Ordinal != i {
			t.Errorf("record %d has Ordinal %d", i, r.Ordinal)
		}
	}
	first, last := records[4], records[5]
	if first.Dir != filepath.Join(repo, "a") || first.Content != "mid" || first.Absent {
		t.Errorf("first PAC = %+v, want repo/a with content mid", first)
	}
	if last.Dir != cwd || last.Content != "leaf" || last.Absent {
		t.Errorf("last PAC = %+v, want the cwd with content leaf (cwd last)", last)
	}
	for _, r := range records {
		if r.Dir == filepath.Join(repo, "a", "b") {
			t.Errorf("repo/a/b holds no tier file but got a record: %+v", r)
		}
	}
}

func TestDiscoverNoRoleShift(t *testing.T) {
	root := resolvedTempDir(t)
	base := filepath.Join(root, "base")
	repo := filepath.Join(base, "proj", "repo")
	cwd := filepath.Join(repo, "app")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "init", "-q")
	plantTier(t, base, "apc-content") // the PPC directory (base/proj) holds none
	plantTier(t, repo, "prc-content")

	records, err := Discover(context.Background(), cwd, fixedHome(filepath.Join(root, "home")))
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	want := []TierRole{TierGCI, TierAPC, TierPPC, TierPRC, TierPAC}
	if !slices.Equal(rolesOf(records), want) || len(records) != 5 {
		t.Fatalf("roles = %v, want exactly %v", rolesOf(records), want)
	}
	if r := records[1]; r.Content != "apc-content" || r.Dir != base {
		t.Errorf("APC = %+v, want base with apc-content (no shift into PPC)", r)
	}
	if r := records[2]; !r.Absent || r.Dir != filepath.Join(base, "proj") {
		t.Errorf("PPC = %+v, want Absent at base/proj", r)
	}
	if r := records[3]; r.Content != "prc-content" {
		t.Errorf("PRC = %+v, want prc-content", r)
	}
	if r := records[4]; !r.Absent || r.Dir != cwd {
		t.Errorf("PAC = %+v, want Absent at the cwd", r)
	}
}

func TestAncestorDotDotCacheSibling(t *testing.T) {
	sep := string(filepath.Separator)
	root := sep + "r"
	if !isProperAncestor(root, filepath.Join(root, "..cache")) {
		t.Error("`..cache` is a child of root, not an escape")
	}
	if !isProperAncestor(root, filepath.Join(root, "..cache", "x")) {
		t.Error("a path below `..cache` is inside root")
	}
	if isProperAncestor(filepath.Join(root, "a"), root) {
		t.Error("a true `..` must not count as below")
	}
	if isProperAncestor(filepath.Join(root, "a"), filepath.Join(root, "b")) {
		t.Error("a sibling must not count as below")
	}

	tmp := resolvedTempDir(t)
	repo := filepath.Join(tmp, "repo")
	cwd := filepath.Join(repo, "..cache")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "init", "-q")
	records, err := Discover(context.Background(), cwd, fixedHome(filepath.Join(tmp, "home")))
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if last := records[len(records)-1]; last.Role != TierPAC || last.Dir != cwd {
		t.Errorf("a `..cache` cwd must be a PAC record, got %+v", last)
	}
}

// countingReader counts Read calls made through wrapTierReader.
type countingReader struct {
	r     io.Reader
	reads *int
}

func (c countingReader) Read(p []byte) (int, error) {
	*c.reads++
	return c.r.Read(p)
}

func countReads(t *testing.T) *int {
	t.Helper()
	n := new(int)
	wrapTierReader = func(r io.Reader) io.Reader {
		return countingReader{r: r, reads: n}
	}
	t.Cleanup(func() { wrapTierReader = func(r io.Reader) io.Reader { return r } })
	return n
}

func TestLoadTierSizeCap(t *testing.T) {
	reads := countReads(t)
	dir := resolvedTempDir(t)
	plantTier(t, dir, string(bytes.Repeat([]byte("x"), maxTierBytes+1)))
	rec, err := loadTier(TierPRC, dir, 3)
	if err != nil {
		t.Fatalf("loadTier: %v", err)
	}
	if !rec.Absent || rec.Content != "" || !slices.Equal(rec.Findings, []DiscoverFinding{FindingTierTooLarge}) {
		t.Errorf("1 MiB + 1 byte: got Absent=%v len=%d findings=%v, want Absent with tier_too_large",
			rec.Absent, len(rec.Content), rec.Findings)
	}
	if *reads != 0 {
		t.Errorf("an oversized tier file was read (%d reads)", *reads)
	}

	atCap := resolvedTempDir(t)
	plantTier(t, atCap, string(bytes.Repeat([]byte("y"), maxTierBytes)))
	rec, err = loadTier(TierPRC, atCap, 3)
	if err != nil || rec.Absent || len(rec.Content) != maxTierBytes || rec.Findings != nil {
		t.Errorf("exactly 1 MiB must load whole: err=%v absent=%v len=%d findings=%v",
			err, rec.Absent, len(rec.Content), rec.Findings)
	}
	if *reads == 0 {
		t.Error("the at-cap file was never read through the counting reader: the counter proves nothing")
	}
}

func TestLoadTierSymlinkAbsent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires elevation on default Windows CI runners")
	}
	reads := countReads(t)
	root := resolvedTempDir(t)
	decoy := filepath.Join(root, "decoy.md")
	if err := os.WriteFile(decoy, []byte("never read"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "repo")
	if err := os.MkdirAll(filepath.Join(dir, tierDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(decoy, filepath.Join(dir, tierDirName, tierFileName)); err != nil {
		t.Fatal(err)
	}
	rec, err := loadTier(TierPRC, dir, 3)
	if err != nil || !rec.Absent || rec.Content != "" || *reads != 0 {
		t.Errorf("symlinked tier file: err=%v rec=%+v reads=%d, want Absent and unread", err, rec, *reads)
	}
}

func TestLoadTierSymlinkSwapAbsent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires elevation on default Windows CI runners")
	}
	reads := countReads(t)
	root := resolvedTempDir(t)
	decoy := filepath.Join(root, "decoy.md")
	if err := os.WriteFile(decoy, []byte("never read"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "repo")
	plantTier(t, dir, "real")
	fired := false
	afterTierLstat = func(path string) {
		fired = true
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(decoy, path); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { afterTierLstat = func(string) {} })

	rec, err := loadTier(TierPRC, dir, 3)
	if !fired {
		t.Fatal("the swap hook never ran; the test proves nothing")
	}
	if err != nil || !rec.Absent || rec.Content != "" || *reads != 0 {
		t.Errorf("swapped tier file: err=%v rec=%+v reads=%d, want Absent and unread", err, rec, *reads)
	}
}

func TestLoadTierSymlinkedCascadeDirAbsent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires elevation on default Windows CI runners")
	}
	reads := countReads(t)
	root := resolvedTempDir(t)
	outside := filepath.Join(root, "outside")
	plantTier(t, outside, "outside content")
	repo := filepath.Join(root, "repo")
	cwd := filepath.Join(repo, "c", "d")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "init", "-q")
	if err := os.Symlink(filepath.Join(outside, tierDirName), filepath.Join(repo, "c", tierDirName)); err != nil {
		t.Fatal(err)
	}
	rec, err := loadTier(TierPAC, filepath.Join(repo, "c"), 4)
	if err != nil || !rec.Absent || rec.Content != "" || *reads != 0 {
		t.Errorf("symlinked .cascade dir: err=%v rec=%+v reads=%d, want Absent and unread", err, rec, *reads)
	}
	records, err := Discover(context.Background(), cwd, fixedHome(filepath.Join(root, "home")))
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(records) != 6 || records[4].Dir != filepath.Join(repo, "c") {
		t.Fatalf("records = %+v, want six with the refused repo/c PAC at index 4", records)
	}
	for _, r := range records {
		if r.Content != "" {
			t.Errorf("%s record at %q read %q through a symlinked .cascade", r.Role, r.Dir, r.Content)
		}
	}
	if *reads != 0 {
		t.Errorf("Discover read through a symlinked .cascade (%d reads)", *reads)
	}
}
