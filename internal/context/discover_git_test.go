package context

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"
)

// Exercises git failure classification: only "not a repository" is silent;
// every other failure surfaces as a finding on the PRC record.

// stubGit puts a `git` script on a PATH that holds nothing else.
func stubGit(t *testing.T, body string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("a shell-script git stand-in needs a POSIX shell")
	}
	dir := t.TempDir()
	script := "#!/bin/sh\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o700); err != nil { //nolint:gosec // executable test stub.
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

func prcFindings(t *testing.T, cwd string) []DiscoverFinding {
	t.Helper()
	records, err := Discover(context.Background(), cwd, fixedHome(filepath.Join(cwd, "nohome")))
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	for _, r := range records {
		if r.Role != TierPRC {
			if r.Findings != nil {
				t.Errorf("%s record carries findings %v; git findings belong on PRC only", r.Role, r.Findings)
			}
			continue
		}
		if r.Dir != cwd {
			t.Errorf("PRC.Dir = %q, want the cwd anchor %q", r.Dir, cwd)
		}
		return r.Findings
	}
	t.Fatal("no PRC record")
	return nil
}

func TestGitErrorClassified(t *testing.T) {
	t.Run("git missing from PATH", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		if _, err := exec.LookPath("git"); err == nil {
			t.Skip("git still resolvable with an empty PATH on this platform")
		}
		got := prcFindings(t, resolvedTempDir(t))
		if !slices.Equal(got, []DiscoverFinding{FindingGitUnavailable}) {
			t.Errorf("findings = %v, want [git_unavailable]", got)
		}
	})
	t.Run("not a repository is silent", func(t *testing.T) {
		if _, err := exec.LookPath("git"); err != nil {
			t.Skip("git not installed")
		}
		if got := prcFindings(t, resolvedTempDir(t)); got != nil {
			t.Errorf("findings = %v, want none for a plain directory", got)
		}
	})
	t.Run("git exits 1", func(t *testing.T) {
		stubGit(t, "exit 1")
		got := prcFindings(t, resolvedTempDir(t))
		if !slices.Equal(got, []DiscoverFinding{FindingGitUnavailable}) {
			t.Errorf("findings = %v, want [git_unavailable]", got)
		}
	})
	t.Run("git reports permission denied", func(t *testing.T) {
		stubGit(t, "echo 'fatal: cannot stat: Permission denied' >&2; exit 128")
		got := prcFindings(t, resolvedTempDir(t))
		if !slices.Equal(got, []DiscoverFinding{FindingGitPermission}) {
			t.Errorf("findings = %v, want [git_permission]", got)
		}
	})
	t.Run("unreadable cwd", checkUnreadableCwd)
}

// lockedDir returns a chmod-000 directory, skipping where permission bits
// cannot make a directory unreadable.
func lockedDir(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits do not model an unreadable directory on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores permission bits")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := filepath.Join(resolvedTempDir(t), "locked")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	return dir
}

// checkUnreadableCwd asserts gitRoot classifies a chmod-000 cwd as
// git_permission and keeps the cwd as the anchor.
func checkUnreadableCwd(t *testing.T) {
	dir := lockedDir(t)
	if got, f := gitRoot(context.Background(), dir); got != dir || f != FindingGitPermission {
		t.Errorf("gitRoot(unreadable) = %q, %q; want the cwd and git_permission", got, f)
	}
}

// TestDiscoverUnreadableCwd holds the unreadable-cwd case through the
// production entry point: records come back and PRC carries git_permission.
func TestDiscoverUnreadableCwd(t *testing.T) {
	dir := lockedDir(t)
	records, err := Discover(context.Background(), dir, fixedHome(filepath.Join(filepath.Dir(dir), "home")))
	if err != nil || len(records) != 5 {
		t.Fatalf("Discover(unreadable cwd): err=%v records=%d, want five records", err, len(records))
	}
	prc := records[3]
	if prc.Role != TierPRC || prc.Dir != dir || !prc.Absent ||
		!slices.Equal(prc.Findings, []DiscoverFinding{FindingGitPermission}) {
		t.Errorf("PRC = %+v, want Absent at the cwd with [git_permission]", prc)
	}
}

// TestDiscoverSymlinkedCwdSeesChain reaches the cwd through a link to the
// repository; the PAC chain between the leaf git root and cwd stays visible.
func TestDiscoverSymlinkedCwdSeesChain(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires elevation on default Windows CI runners")
	}
	root := resolvedTempDir(t)
	repo := filepath.Join(root, "repo")
	leaf := filepath.Join(repo, "a", "b")
	if err := os.MkdirAll(leaf, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "init", "-q")
	plantTier(t, filepath.Join(repo, "a"), "a")
	plantTier(t, leaf, "ab")
	lnk := filepath.Join(root, "lnk")
	if err := os.Symlink(repo, lnk); err != nil {
		t.Fatal(err)
	}
	records, err := Discover(context.Background(), filepath.Join(lnk, "a", "b"), fixedHome(filepath.Join(root, "home")))
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	want := []TierRole{TierGCI, TierAPC, TierPPC, TierPRC, TierPAC, TierPAC}
	if !slices.Equal(rolesOf(records), want) {
		t.Fatalf("roles = %v, want %v", rolesOf(records), want)
	}
	if r := records[4]; r.Content != "a" || r.Dir != filepath.Join(repo, "a") {
		t.Errorf("first PAC = %+v, want repo/a with content a", r)
	}
	if r := records[5]; r.Content != "ab" || r.Dir != leaf {
		t.Errorf("cwd PAC = %+v, want repo/a/b with content ab", r)
	}
}

// mkfifo makes a FIFO at path with the system mkfifo, skipping where FIFOs
// or the tool do not exist.
func mkfifo(t *testing.T, path string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("FIFOs are a POSIX feature")
	}
	bin, err := exec.LookPath("mkfifo")
	if err != nil {
		t.Skip("mkfifo not installed")
	}
	if out, err := exec.CommandContext(context.Background(), bin, path).CombinedOutput(); err != nil {
		t.Fatalf("mkfifo: %v\n%s", err, out)
	}
}

// returnsWithin runs fn and reports whether it returned inside 5 s, so a
// blocking open fails the test instead of hanging it.
func returnsWithin(t *testing.T, what string, fn func()) bool {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); fn() }()
	select {
	case <-done:
		return true
	case <-time.After(5 * time.Second):
		t.Errorf("%s blocked for 5 s on a FIFO tier file", what)
		return false
	}
}

func TestLoadTierFIFORefused(t *testing.T) {
	dir := resolvedTempDir(t)
	fifo := filepath.Join(dir, tierDirName, tierFileName)
	if err := os.MkdirAll(filepath.Dir(fifo), 0o755); err != nil {
		t.Fatal(err)
	}
	mkfifo(t, fifo)
	reachedOpen := false
	afterTierLstat = func(string) { reachedOpen = true }
	t.Cleanup(func() { afterTierLstat = func(string) {} })

	var rec TierRecord
	var err error
	if !returnsWithin(t, "loadTier", func() { rec, err = loadTier(TierPRC, dir, 3) }) {
		return
	}
	if err != nil || !rec.Absent || rec.Content != "" || rec.Findings != nil {
		t.Errorf("FIFO tier file: err=%v rec=%+v, want Absent, unread, no finding", err, rec)
	}
	if reachedOpen {
		t.Error("a FIFO tier file reached the open; it must be refused at the Lstat")
	}
	var f *os.File
	if !returnsWithin(t, "openNoFollow", func() { f, err = openNoFollow(fifo) }) {
		return
	}
	if err != nil {
		t.Fatalf("openNoFollow(FIFO): %v, want a non-blocking open", err)
	}
	_ = f.Close()
}

func TestDiscoverFIFOTierDoesNotHang(t *testing.T) {
	root := resolvedTempDir(t)
	repo := filepath.Join(root, "repo")
	runGit(t, repo, "init", "-q")
	if err := os.MkdirAll(filepath.Join(repo, tierDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	mkfifo(t, filepath.Join(repo, tierDirName, tierFileName))
	var records []TierRecord
	var err error
	if !returnsWithin(t, "Discover", func() {
		records, err = Discover(context.Background(), repo, fixedHome(filepath.Join(root, "home")))
	}) {
		return
	}
	if err != nil || len(records) != 5 {
		t.Fatalf("Discover: err=%v records=%d, want five records", err, len(records))
	}
	if r := records[3]; r.Role != TierPRC || r.Dir != repo || !r.Absent || r.Content != "" {
		t.Errorf("PRC = %+v, want the FIFO refused as Absent", r)
	}
}
