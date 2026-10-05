package context

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose: GitCommonDir is the one production implementation of
//   internal/context/scope.GitCommonDirFunc. The A-T2 exec allowlist
//   (internal/build/egress_allow.go) admits this package, not
//   internal/context/scope, so the real git invocations live here and
//   every caller injects this function.
// Inputs: a context.Context (for the subprocesses) and dir, the directory
//   to resolve.
// Outputs: scope.GitDirs for dir, or a typed cascade.Error:
//   KindInvalidInput when dir is not inside a git repository,
//   KindUnavailable when git could not run, timed out, exited in a way
//   this function does not read as "unset", or printed output it does
//   not understand.
// Constraints: bounded by gitExecTimeout independent of ctx, plus
//   gitWaitDelay for a child that forks and leaves stdout open; every
//   `git rev-parse --local-env-vars` variable (gitLocalEnvVars) is
//   stripped from every child's environment so a caller's git context
//   never replaces dir's own repository.
// SPORT: context/repo-identity/ADD.

// gitExecTimeout bounds each GitCommonDir and GitRemoteURL call so a hung
// git (a lock, a credential prompt) never hangs the caller. A variable
// only so tests can shorten it.
var gitExecTimeout = 5 * time.Second

// gitWaitDelay caps how long a killed git's leftover children may hold its
// output pipes open: without it a git that forks (a wrapper script, a
// credential helper) keeps Output blocked until the grandchild exits.
const gitWaitDelay = time.Second

// GitCommonDir resolves dir's GitDirs from `git rev-parse
// --path-format=absolute --git-common-dir --absolute-git-dir`, core.bare
// from <CommonDir>/config, then `--show-toplevel` when not bare and
// core.worktree from the same file.
func GitCommonDir(ctx context.Context, dir string) (scope.GitDirs, error) {
	ctx, cancel := context.WithTimeout(ctx, gitExecTimeout)
	defer cancel()

	out, err := gitOutput(ctx, dir, "rev-parse", "--path-format=absolute",
		"--git-common-dir", "--absolute-git-dir")
	if err != nil {
		return scope.GitDirs{}, classifyGitCommonDirErr(dir, err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 2 {
		return scope.GitDirs{}, cascade.Newf(cascade.KindUnavailable,
			"context: unexpected git rev-parse output in %q: %q", dir, string(out))
	}
	d := scope.GitDirs{CommonDir: cleanGitPath(lines[0]), GitDir: cleanGitPath(lines[1])}
	if d.Bare, err = gitCommonDirBare(ctx, dir, d.CommonDir); err != nil {
		return scope.GitDirs{}, err
	}
	if !d.Bare {
		if d.Toplevel, err = gitToplevel(ctx, dir); err != nil {
			return scope.GitDirs{}, err
		}
	}
	if d.CoreWorktree, err = gitCoreWorktree(ctx, dir, d.CommonDir); err != nil {
		return scope.GitDirs{}, err
	}
	return d, nil
}

// gitToplevel runs `git rev-parse --show-toplevel`; "must be run in a
// work tree" (a path inside a .git directory) is "" with no error, any
// other failure is KindUnavailable.
func gitToplevel(ctx context.Context, dir string) (string, error) {
	out, err := gitOutput(ctx, dir, "rev-parse", "--show-toplevel")
	if err == nil {
		return cleanGitPath(string(out)), nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && strings.Contains(string(exitErr.Stderr), "must be run in a work tree") {
		return "", nil
	}
	return "", cascade.Wrapf(cascade.KindUnavailable, err, "context: git rev-parse --show-toplevel in %q", dir)
}

// gitCommonDirBare reads core.bare from <commonDir>/config, so a linked
// worktree of a bare repository reports Bare like the bare dir itself.
// Only an explicit true is bare: exit 1 (unset) is not bare; any other
// failure is KindUnavailable, never read as "not bare".
func gitCommonDirBare(ctx context.Context, dir, commonDir string) (bool, error) {
	out, err := gitOutput(ctx, dir, "config", "--file",
		filepath.Join(commonDir, "config"), "--type=bool", "--get", "core.bare")
	if err == nil {
		return strings.TrimSpace(string(out)) == "true", nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, cascade.Wrapf(cascade.KindUnavailable, err, "context: git config core.bare for %q", dir)
}

// gitCoreWorktree reads the raw core.worktree from <commonDir>/config.
// Exit 1 means unset (""); any other failure is KindUnavailable, never
// read as unset.
func gitCoreWorktree(ctx context.Context, dir, commonDir string) (string, error) {
	out, err := gitOutput(ctx, dir, "config", "--file",
		filepath.Join(commonDir, "config"), "--get", "core.worktree")
	if err == nil {
		return strings.TrimSpace(string(out)), nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return "", nil
	}
	return "", cascade.Wrapf(cascade.KindUnavailable, err, "context: git config core.worktree for %q", dir)
}

// gitOutput runs git with args in dir with gitLocalEnvVars removed from
// the environment, returning stdout.
func gitOutput(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = stripGitDirEnv(os.Environ())
	cmd.WaitDelay = gitWaitDelay
	return cmd.Output()
}

func cleanGitPath(s string) string {
	return filepath.Clean(filepath.FromSlash(strings.TrimSpace(s)))
}

// classifyGitCommonDirErr maps a failed `git rev-parse`: "not a git
// repository" is caller input (KindInvalidInput); anything else -- git
// missing, killed by the timeout, another non-zero exit -- is
// KindUnavailable.
func classifyGitCommonDirErr(dir string, err error) error {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		stderr := strings.TrimSpace(string(exitErr.Stderr))
		if strings.Contains(stderr, "not a git repository") {
			return cascade.Newf(cascade.KindInvalidInput,
				"context: %q is not a git repository: %s", dir, stderr)
		}
		return cascade.Wrapf(cascade.KindUnavailable, err,
			"context: git rev-parse failed in %q: %s", dir, stderr)
	}
	return cascade.Wrapf(cascade.KindUnavailable, err, "context: run git in %q", dir)
}
