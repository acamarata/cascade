package ci

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestBranchObjectID(t *testing.T) {
	for id, want := range map[string]bool{
		strings.Repeat("a", 40): true,
		strings.Repeat("0", 64): true,
		strings.Repeat("a", 39): false,
		strings.Repeat("A", 40): false,
		strings.Repeat("g", 40): false,
		"":                      false,
	} {
		if got := branchObjectID(id); got != want {
			t.Errorf("branchObjectID(%q) = %v, want %v", id, got, want)
		}
	}
}

// notARepo is a directory git cannot read as a repository.
func notARepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("USERPROFILE", root)
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(root))
	return root
}

func TestBaselineGitReadsRefuseANonRepository(t *testing.T) {
	root, ctx := notARepo(t), context.Background()
	if _, err := LocalBranchTips(ctx, root); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Errorf("LocalBranchTips = %v", err)
	}
	if _, _, err := gitCheckedOutBranch(ctx, root); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Errorf("gitCheckedOutBranch = %v", err)
	}
}

func TestGitCheckedOutBranch(t *testing.T) {
	root := baselineRepo(t)
	commit := baselineCommit(t, root)
	ref, detached, err := gitCheckedOutBranch(context.Background(), root)
	if err != nil || detached || ref != "refs/heads/main" {
		t.Fatalf("on a branch = %q %v %v", ref, detached, err)
	}
	baselineGitTest(t, root, "checkout", "--detach", commit)
	if ref, detached, err = gitCheckedOutBranch(context.Background(), root); err != nil || !detached || ref != "" {
		t.Fatalf("detached = %q %v %v", ref, detached, err)
	}
}
