//go:build spike

package syncmerge

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// testGit runs a real git subprocess with a fixed, hermetic identity (no
// reliance on any global ~/.gitconfig) so this test is deterministic on any
// machine (Art.7).
func testGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{
		"-c", "user.name=spike",
		"-c", "user.email=spike@example.invalid",
		"-c", "init.defaultBranch=main",
	}, args...)
	cmd := exec.CommandContext(context.Background(), "git", full...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// initRepoWithFile creates a fresh git repo at dir, containing one commit
// carrying phase.yaml with the given content.
func initRepoWithFile(t *testing.T, dir, content string) {
	t.Helper()
	testGit(t, dir, "init")
	writePhaseFile(t, dir, content)
	testGit(t, dir, "add", "phase.yaml")
	testGit(t, dir, "commit", "-m", "initial")
}

func writePhaseFile(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "phase.yaml"), []byte(content), 0o644); err != nil {
		t.Fatalf("write phase.yaml: %v", err)
	}
}

// TestPhaseMergeGit exercises phaseMergeGit against real two-branch git
// repos in t.TempDir(): clean fast-forward, non-fast-forward divergence
// (refused with ErrPhaseStateDiverged), and disjoint branches (also
// refused, never auto-merged). git --version and the date are stamped in
// testdata/README.md per Art.2.2.
func TestPhaseMergeGit(t *testing.T) {
	t.Run("clean-fast-forward", testPhaseCleanFastForward)
	t.Run("non-fast-forward-diverged", testPhaseNonFastForwardDiverged)
	t.Run("disjoint-branches", testPhaseDisjointBranches)
}

func testPhaseCleanFastForward(t *testing.T) {
	ctx := context.Background()
	local := t.TempDir()
	remote := t.TempDir()
	initRepoWithFile(t, local, "phase: 1\n")
	testGit(t, local, "clone", "--bare", local+"/.git", remote+"/.git")

	// Advance the remote by cloning it out, committing, and pushing back,
	// so remote strictly fast-forwards from local's current HEAD.
	work := t.TempDir()
	testGit(t, work, "clone", remote+"/.git", ".")
	writePhaseFile(t, work, "phase: 2\n")
	testGit(t, work, "add", "phase.yaml")
	testGit(t, work, "commit", "-m", "advance")
	testGit(t, work, "push", "origin", "HEAD:refs/heads/main")

	result, err := phaseMergeGit(ctx, local, remote+"/.git", "main")
	if err != nil {
		t.Fatalf("phaseMergeGit clean fast-forward: %v", err)
	}
	if !result.FastForwarded {
		t.Fatalf("expected FastForwarded=true, got %+v", result)
	}
	got, err := os.ReadFile(filepath.Join(local, "phase.yaml"))
	if err != nil {
		t.Fatalf("read merged phase.yaml: %v", err)
	}
	if string(got) != "phase: 2\n" {
		t.Fatalf("phase.yaml after fast-forward = %q, want %q", got, "phase: 2\n")
	}
}

func testPhaseNonFastForwardDiverged(t *testing.T) {
	ctx := context.Background()
	local := t.TempDir()
	initRepoWithFile(t, local, "phase: 1\n")
	remote := t.TempDir()
	testGit(t, local, "clone", "--bare", local+"/.git", remote+"/.git")

	// Diverge LOCAL with its own commit.
	writePhaseFile(t, local, "phase: local-2\n")
	testGit(t, local, "add", "phase.yaml")
	testGit(t, local, "commit", "-m", "local advance")

	// Diverge REMOTE independently via a separate clone.
	work := t.TempDir()
	testGit(t, work, "clone", remote+"/.git", ".")
	writePhaseFile(t, work, "phase: remote-2\n")
	testGit(t, work, "add", "phase.yaml")
	testGit(t, work, "commit", "-m", "remote advance")
	testGit(t, work, "push", "origin", "HEAD:refs/heads/main")

	_, err := phaseMergeGit(ctx, local, remote+"/.git", "main")
	if err == nil {
		t.Fatalf("expected ErrPhaseStateDiverged for a non-fast-forward merge, got nil")
	}
	if !errors.Is(err, ErrPhaseStateDiverged) {
		t.Fatalf("phaseMergeGit non-fast-forward: got %v, want ErrPhaseStateDiverged", err)
	}
}

func testPhaseDisjointBranches(t *testing.T) {
	ctx := context.Background()
	local := t.TempDir()
	initRepoWithFile(t, local, "phase: local-only\n")

	remote := t.TempDir()
	testGit(t, remote, "init")
	if err := os.WriteFile(filepath.Join(remote, "other.yaml"), []byte("unrelated: true\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	testGit(t, remote, "add", "other.yaml")
	testGit(t, remote, "commit", "-m", "disjoint history")

	_, err := phaseMergeGit(ctx, local, remote, "main")
	if err == nil {
		t.Fatalf("expected ErrPhaseStateDiverged for disjoint unrelated histories, got nil")
	}
	if !errors.Is(err, ErrPhaseStateDiverged) {
		t.Fatalf("phaseMergeGit disjoint branches: got %v, want ErrPhaseStateDiverged", err)
	}
}
