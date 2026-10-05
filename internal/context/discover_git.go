package context

import (
	"context"
	"errors"
	"io/fs"
	"os/exec"
	"path/filepath"
	"strings"
)

// Purpose: locate the git root that anchors tier discovery, and classify a
//   failed git invocation so only "not a repository" is silent.
// Inputs: a context.Context and a working directory.
// Outputs: the anchor directory and a DiscoverFinding ("" when none).
// Constraints: never returns an error; the anchor falls back to cwd.
// SPORT: context-engine/discovery.

// gitRoot invokes `git rev-parse --show-toplevel` in cwd and returns the
// repository root. On failure it returns cwd as the anchor and classifies the
// failure: "not a repository" is the normal no-repo case and yields no
// finding; a permission failure yields FindingGitPermission; anything else
// (git missing, exec failure, other exit) yields FindingGitUnavailable.
//
// git always prints the toplevel with forward slashes, even on Windows;
// filepath.FromSlash converts it before any filepath comparison.
func gitRoot(ctx context.Context, cwd string) (string, DiscoverFinding) {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--show-toplevel")
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		return cwd, classifyGitErr(err)
	}
	root := strings.TrimSpace(string(out))
	if root == "" {
		return cwd, FindingGitUnavailable
	}
	return filepath.Clean(filepath.FromSlash(root)), ""
}

// classifyGitErr maps a failed git invocation to a finding ("" = not_repo,
// the only silent class).
func classifyGitErr(err error) DiscoverFinding {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		stderr := string(ee.Stderr)
		switch {
		case strings.Contains(stderr, "Permission denied"):
			return FindingGitPermission
		case ee.ExitCode() == 128 && strings.Contains(stderr, "not a git repository"):
			return ""
		}
		return FindingGitUnavailable
	}
	if errors.Is(err, fs.ErrPermission) {
		return FindingGitPermission
	}
	return FindingGitUnavailable
}
