// TestPrePushHookRealGit (P1-E01-W1-S01-T5) is the real-counterpart
// integration test Art.2 requires for the pre-push hook: it never invents
// its own stand-in for git's pre-push protocol, it drives the actual
// .github/hooks/pre-push script through an actual `git push` against an
// actual bare remote. Split from sweep_test.go under R-14.117 (the
// combined file would exceed Art.10.3's 300-line cap).
//
// # Why the fixture is a full tracked-files copy, not a stub package
//
// The shipped hook runs `go test -run ... ./internal/build/...` against
// wherever `git rev-parse --show-toplevel` resolves to. For that to be a
// real, non-mocked exercise of commits.go/sweep.go, the throwaway repo
// needs a real, buildable copy of this module — not just the two new
// files. materializeTrackedRepo copies exactly what `git ls-files`
// reports from the real repo (536 tracked files, ~13MB at the time this
// ticket landed), which is also, not incidentally, the exact "tracked
// files" universe the identifier sweep itself is defined over.
package build

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// materializeTrackedRepo copies every file `git ls-files` reports from
// srcRoot into a fresh directory under t.TempDir() (Art.7.1) and returns
// its path. The copy is NOT yet a git repo — the caller runs `git init`.
func materializeTrackedRepo(t *testing.T, srcRoot string) string {
	t.Helper()
	files, err := ListTrackedFiles(srcRoot)
	if err != nil {
		t.Fatalf("materializeTrackedRepo: %v", err)
	}
	dst := t.TempDir()
	for _, rel := range files {
		src := filepath.Join(srcRoot, rel)
		data, err := os.ReadFile(src)
		if err != nil {
			t.Fatalf("materializeTrackedRepo: reading %s: %v", rel, err)
		}
		info, err := os.Stat(src)
		if err != nil {
			t.Fatalf("materializeTrackedRepo: stat %s: %v", rel, err)
		}
		target := filepath.Join(dst, rel)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatalf("materializeTrackedRepo: mkdir for %s: %v", rel, err)
		}
		// Preserve the source file's mode (in particular the executable
		// bit on .github/hooks/pre-push — git's core.hooksPath refuses to
		// run a non-executable hook, silently treating it as absent).
		if err := os.WriteFile(target, data, info.Mode().Perm()); err != nil {
			t.Fatalf("materializeTrackedRepo: writing %s: %v", rel, err)
		}
	}
	return dst
}

// runPush attempts `git push origin HEAD` from dir with env appended to the
// current process environment, returning the combined output and whether
// the command succeeded — never fatal on a non-zero exit, since callers
// assert on that exit status themselves.
func runPush(t *testing.T, dir string, env []string) (string, bool) {
	t.Helper()
	cmd := exec.Command("git", "push", "origin", "HEAD")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	return string(out), err == nil
}

// TestPrePushHookRealGit seeds a throwaway repo (a real tracked-files copy
// of this module) with a clean baseline commit and then a commit whose
// message fails the conventional-commit gate AND whose content trips the
// identifier sweep, installs the real pre-push hook via core.hooksPath,
// and asserts a real `git push` is rejected (non-zero exit). It then fixes
// the offending commit and asserts the same push path succeeds — "clean
// ranges pass both" (T-5 AC).
func TestPrePushHookRealGit(t *testing.T) {
	if testing.Short() {
		t.Skip("materializes a full tracked-files copy of the module; skipped under -short")
	}
	srcRoot := sweepModuleRoot(t)
	repo := materializeTrackedRepo(t, srcRoot)

	runGit(t, repo, "init", "-q", "-b", "main")
	runGit(t, repo, "config", "user.email", "fixture@example.invalid")
	runGit(t, repo, "config", "user.name", "Fixture")
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-q", "-m", "chore: seed baseline")

	// The bad commit: non-conventional message, and file content tripping
	// a pattern the test supplies via CASCADE_IDENTIFIER_PATTERNS_FILE
	// below (never a real identifier, per this file's package doc).
	writeFileT(t, filepath.Join(repo, "hygiene-fixture.txt"), "marker SEEDED-IDENTIFIER-LEAK-0001 present\n")
	runGit(t, repo, "add", "hygiene-fixture.txt")
	runGit(t, repo, "commit", "-q", "-m", "add fixture without a conventional type")

	bareRemote := filepath.Join(t.TempDir(), "remote.git")
	if err := os.MkdirAll(bareRemote, 0o755); err != nil {
		t.Fatalf("mkdir bare remote: %v", err)
	}
	runGit(t, bareRemote, "init", "-q", "--bare")
	runGit(t, repo, "remote", "add", "origin", bareRemote)
	runGit(t, repo, "config", "core.hooksPath", ".github/hooks")

	patternsFile := filepath.Join(t.TempDir(), "patterns.txt")
	writeFileT(t, patternsFile, "SEEDED-IDENTIFIER-LEAK-\\d+\n")
	env := []string{"CASCADE_IDENTIFIER_PATTERNS_FILE=" + patternsFile}

	out, ok := runPush(t, repo, env)
	if ok {
		t.Fatalf("expected the pre-push hook to reject the seeded bad commit, but push succeeded:\n%s", out)
	}
	t.Logf("pre-push hook correctly rejected the seeded violation:\n%s", out)

	// Fix: amend the bad commit to a clean, conventional message and drop
	// the identifier-tripping content, then push again and expect success.
	writeFileT(t, filepath.Join(repo, "hygiene-fixture.txt"), "marker removed\n")
	runGit(t, repo, "add", "hygiene-fixture.txt")
	runGit(t, repo, "commit", "-q", "--amend", "-m", "chore(build): add clean hygiene fixture")

	out, ok = runPush(t, repo, env)
	if !ok {
		t.Fatalf("expected the pre-push hook to pass a clean range, but push failed:\n%s", out)
	}
	t.Logf("pre-push hook correctly accepted the clean range:\n%s", out)
}
