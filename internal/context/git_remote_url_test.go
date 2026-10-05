package context

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestGitRemoteURLRealRepo (the CI ledger route): a repository made
// with the real git binary under t.TempDir() with an origin remote
// returns exactly that url; a repository with no origin, and a
// non-repository directory, each return "". GIT_CONFIG_GLOBAL/NOSYSTEM
// and GIT_CEILING_DIRECTORIES are set so no ambient config or parent-dir
// walk can affect the result.
func TestGitRemoteURLRealRepo(t *testing.T) {
	skipWithoutGit(t)

	dir := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))

	const wantURL = "https://example.invalid/o/r.git"
	runGit(t, dir, "init", "-q")
	runGit(t, dir, "remote", "add", "origin", wantURL)

	if got := GitRemoteURL(context.Background(), dir); got != wantURL {
		t.Errorf("GitRemoteURL(origin set) = %q, want %q", got, wantURL)
	}

	noOrigin := t.TempDir()
	runGit(t, noOrigin, "init", "-q")
	if got := GitRemoteURL(context.Background(), noOrigin); got != "" {
		t.Errorf("GitRemoteURL(no origin) = %q, want \"\"", got)
	}

	nonRepo := t.TempDir()
	if got := GitRemoteURL(context.Background(), nonRepo); got != "" {
		t.Errorf("GitRemoteURL(non-repository) = %q, want \"\"", got)
	}
}

// TestGitRemoteURLIgnoresGitDirEnv proves GIT_DIR/GIT_WORK_TREE pointing
// at another repository never replace root's own origin.
func TestGitRemoteURLIgnoresGitDirEnv(t *testing.T) {
	skipWithoutGit(t)
	base := t.TempDir()
	isolateGit(t, base)
	repoA, repoB := newRealRepo(t, base, "a"), newRealRepo(t, base, "b")
	runGit(t, repoA, "remote", "add", "origin", "https://example.invalid/a.git")
	runGit(t, repoB, "remote", "add", "origin", "https://example.invalid/b.git")
	t.Setenv("GIT_DIR", filepath.Join(repoB, ".git"))
	t.Setenv("GIT_WORK_TREE", repoB)

	if got := GitRemoteURL(context.Background(), repoA); got != "https://example.invalid/a.git" {
		t.Errorf("GitRemoteURL(a) with GIT_DIR=b = %q, want a's origin", got)
	}
}

// TestGitRemoteURLBounded proves a hung git is cut off by gitExecTimeout
// and reads as "" (no origin), never a hang.
func TestGitRemoteURLBounded(t *testing.T) {
	fakeHungGit(t, hungGitScript)
	start := time.Now()
	got := GitRemoteURL(context.Background(), t.TempDir())
	if elapsed := time.Since(start); elapsed > 5*time.Second || got != "" {
		t.Errorf("GitRemoteURL with a hung git = %q after %v, want \"\" within %v", got, elapsed, gitExecTimeout)
	}
}

// hungGitScript sleeps in place of git (exec, so the killed process is
// the sleeper); forkingGitScript sleeps in a child that keeps the output
// pipes open after the timeout kills the shell.
const (
	hungGitScript    = "#!/bin/sh\nexec /bin/sleep 20\n"
	forkingGitScript = "#!/bin/sh\n/bin/sleep 20\n"
)

// fakeHungGit puts a `git` running script first (and alone) on PATH, and
// shortens gitExecTimeout after checking its 5s contract value.
func fakeHungGit(t *testing.T, script string) {
	t.Helper()
	if gitExecTimeout != 5*time.Second {
		t.Fatalf("gitExecTimeout = %v, want the 5s contract value", gitExecTimeout)
	}
	installFakeGit(t, script)
	old := gitExecTimeout
	gitExecTimeout = 300 * time.Millisecond
	t.Cleanup(func() { gitExecTimeout = old })
}

// installFakeGit writes script as the only `git` on PATH.
func installFakeGit(t *testing.T, script string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake git is a POSIX shell script")
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake git: %v", err)
	}
	t.Setenv("PATH", bin)
}

// TestGitBoundedForkingFake proves a git whose child keeps stdout open
// after the timeout still returns within gitExecTimeout + gitWaitDelay
// (plus scheduling margin), for both GitCommonDir and GitRemoteURL. The
// limit grows with gitWaitDelay, so its 1s contract value is checked first.
func TestGitBoundedForkingFake(t *testing.T) {
	if gitWaitDelay != time.Second {
		t.Fatalf("gitWaitDelay = %v, want the 1s contract value", gitWaitDelay)
	}
	fakeHungGit(t, forkingGitScript)
	limit := gitExecTimeout + gitWaitDelay + 1500*time.Millisecond
	start := time.Now()
	_, err := GitCommonDir(context.Background(), t.TempDir())
	if elapsed := time.Since(start); elapsed > limit || err == nil {
		t.Errorf("GitCommonDir with a forking hung git: %v after %v, want an error within %v", err, elapsed, limit)
	}
	start = time.Now()
	got := GitRemoteURL(context.Background(), t.TempDir())
	if elapsed := time.Since(start); elapsed > limit || got != "" {
		t.Errorf("GitRemoteURL with a forking hung git = %q after %v, want \"\" within %v", got, elapsed, limit)
	}
}

// TestGitLocalEnvVarsCoverLiveGit compares gitLocalEnvVars with the
// installed git's `git rev-parse --local-env-vars` and proves
// stripGitDirEnv removes every one of them.
func TestGitLocalEnvVarsCoverLiveGit(t *testing.T) {
	skipWithoutGit(t)
	out, err := exec.Command("git", "rev-parse", "--local-env-vars").Output()
	if err != nil {
		t.Fatalf("git rev-parse --local-env-vars: %v", err)
	}
	live := strings.Fields(string(out))
	if len(live) == 0 {
		t.Fatal("git rev-parse --local-env-vars printed nothing")
	}
	env := []string{"PATH=/bin", "GIT_AUTHOR_NAME=kept"}
	for _, name := range live {
		if !slices.Contains(gitLocalEnvVars, name) {
			t.Errorf("gitLocalEnvVars lacks %s, which this git prints", name)
		}
		env = append(env, name+"=/elsewhere")
	}
	if got := stripGitDirEnv(env); !slices.Equal(got, []string{"PATH=/bin", "GIT_AUTHOR_NAME=kept"}) {
		t.Errorf("stripGitDirEnv kept %v, want only PATH and GIT_AUTHOR_NAME", got)
	}
}
