package ci

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func baselineGitTest(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil && errors.Is(err, os.ErrPermission) {
		t.Skipf("git cannot be executed here: %v", err)
	}
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func baselineRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("USERPROFILE", root)
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(root))
	baselineGitTest(t, root, "init", "-b", "main")
	baselineGitTest(t, root, "config", "user.name", "Test")
	baselineGitTest(t, root, "config", "user.email", "test@example.invalid")
	return root
}

func baselineCommit(t *testing.T, root string) string {
	t.Helper()
	baselineGitTest(t, root, "commit", "--allow-empty", "-m", "checkpoint")
	return baselineGitTest(t, root, "rev-parse", "HEAD")
}

func TestLocalBranchTipsRealRepo(t *testing.T) {
	root := baselineRepo(t)
	tips, err := LocalBranchTips(context.Background(), root)
	if err != nil || tips == nil || len(tips) != 0 {
		t.Fatalf("unborn: %v %v", tips, err)
	}
	main := baselineCommit(t, root)
	baselineGitTest(t, root, "checkout", "-b", "feature")
	feature := baselineCommit(t, root)
	tips, err = LocalBranchTips(context.Background(), root)
	if err != nil || len(tips) != 2 || tips[0].Ref != "refs/heads/feature" || tips[0].Commit != feature || tips[1].Commit != main {
		t.Fatalf("tips: %v %v", tips, err)
	}
	wt := filepath.Join(t.TempDir(), "linked")
	baselineGitTest(t, root, "worktree", "add", wt, "main")
	linked, err := LocalBranchTips(context.Background(), wt)
	if err != nil || len(linked) != 2 || linked[0] != tips[0] || linked[1] != tips[1] {
		t.Fatalf("linked: %v %v", linked, err)
	}
	if _, err := LocalBranchTips(context.Background(), t.TempDir()); err == nil {
		t.Fatal("non-repository accepted")
	}
}
