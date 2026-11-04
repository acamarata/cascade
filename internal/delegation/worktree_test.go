package delegation

// Purpose (this file): tests for worktree allocation.
// Verifies minimal checkout for mutation, empty 0700 for read-only,
// release before lease, and git cwd requirement for mutations.
//
// SPORT: delegation.worktree (ADD, P1-AGT-08).

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDelegateAllocatesMinimalWorktree verifies mutation worktrees
// contain only job/<job id> and no extraneous paths.
func TestDelegateAllocatesMinimalWorktree(t *testing.T) {
	base := t.TempDir()
	wt, err := AllocWorktree(base, "job-123", true)
	if err != nil {
		t.Fatalf("AllocWorktree failed: %v", err)
	}
	defer func() { _ = wt.Release() }()

	// Verify structure
	expectedRoot := filepath.Join(base, "job", "job-123")
	if wt.Root != expectedRoot {
		t.Errorf("Root = %q, want %q", wt.Root, expectedRoot)
	}
	if wt.GitCwd != expectedRoot {
		t.Errorf("GitCwd = %q, want %q", wt.GitCwd, expectedRoot)
	}
	if wt.ReadOnly {
		t.Error("ReadOnly = true, want false for mutation")
	}

	// Verify .git exists
	gitDir := filepath.Join(wt.Root, ".git")
	if info, err := os.Stat(gitDir); err != nil || !info.IsDir() {
		t.Errorf(".git directory missing or not a dir: %v", err)
	}

	// Verify no extra files outside job/<id>
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatalf("ReadDir base: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "job" {
		t.Errorf("base contains unexpected entries: %v", entries)
	}
}

// TestDelegateReadOnlyGetsEmptyTree verifies read-only worktrees
// are empty directories with 0700 permissions.
func TestDelegateReadOnlyGetsEmptyTree(t *testing.T) {
	base := t.TempDir()
	wt, err := AllocWorktree(base, "ro-job", false)
	if err != nil {
		t.Fatalf("AllocWorktree failed: %v", err)
	}
	defer func() { _ = wt.Release() }()

	if !wt.ReadOnly {
		t.Error("ReadOnly = false, want true")
	}
	if wt.GitCwd != "" {
		t.Errorf("GitCwd = %q, want empty for read-only", wt.GitCwd)
	}

	// Check permissions
	info, err := os.Stat(wt.Root)
	if err != nil {
		t.Fatalf("Stat root: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("permissions = %o, want 700", perm)
	}

	// Check empty
	entries, err := os.ReadDir(wt.Root)
	if err != nil {
		t.Fatalf("ReadDir root: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("read-only dir not empty: %v", entries)
	}
}

// TestDelegateReleasesWorktreeBeforeLease verifies Release removes
// the worktree from disk.
func TestDelegateReleasesWorktreeBeforeLease(t *testing.T) {
	base := t.TempDir()
	wt, err := AllocWorktree(base, "release-test", true)
	if err != nil {
		t.Fatalf("AllocWorktree failed: %v", err)
	}

	root := wt.Root
	if err := wt.Release(); err != nil {
		t.Fatalf("Release failed: %v", err)
	}

	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Errorf("worktree still exists after Release: %v", err)
	}

	// Double release should be safe
	if err := wt.Release(); err != nil {
		t.Errorf("double Release failed: %v", err)
	}
}

// TestDelegateMutationRequiresGitCwd verifies mutation worktrees
// have a non-empty GitCwd.
func TestDelegateMutationRequiresGitCwd(t *testing.T) {
	base := t.TempDir()
	wt, err := AllocWorktree(base, "git-cwd-test", true)
	if err != nil {
		t.Fatalf("AllocWorktree failed: %v", err)
	}
	defer func() { _ = wt.Release() }()

	if wt.GitCwd == "" {
		t.Error("GitCwd is empty for mutation worktree")
	}
	if wt.GitCwd != wt.Root {
		t.Errorf("GitCwd = %q, want Root %q", wt.GitCwd, wt.Root)
	}

	// Read-only should have empty GitCwd
	ro, err := AllocWorktree(base, "ro-git", false)
	if err != nil {
		t.Fatalf("AllocWorktree ro failed: %v", err)
	}
	defer func() { _ = ro.Release() }()
	if ro.GitCwd != "" {
		t.Errorf("read-only GitCwd = %q, want empty", ro.GitCwd)
	}
}

// TestCoverageGapDelegationBranches covers remaining untested branches in worktree.go.
func TestCoverageGapDelegationBranches(t *testing.T) {
	t.Run("AllocWorktree_empty_jobid", func(t *testing.T) {
		base := t.TempDir()
		wt, err := AllocWorktree(base, "", true)
		if wt != nil || err == nil {
			t.Fatalf("AllocWorktree empty job = %+v, %v; want error", wt, err)
		}
	})

	t.Run("AllocWorktree_mkdir_fail", func(t *testing.T) {
		base := t.TempDir()
		blocker := filepath.Join(base, "job")
		if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		wt, err := AllocWorktree(base, "fail-mkdir", true)
		if wt != nil || err == nil {
			t.Fatalf("AllocWorktree mkdir fail = %+v, %v; want error", wt, err)
		}
	})

	t.Run("Release_already_gone", func(t *testing.T) {
		wt := &Worktree{Root: filepath.Join(t.TempDir(), "nonexistent-dir-that-does-not-exist")}
		err := wt.Release()
		if err != nil {
			t.Fatalf("Release of nonexistent root = %v; want nil (idempotent)", err)
		}
	})
}

// TestAllocWorktreeBlockedPaths covers the mkdir failures and the Release error.
func TestAllocWorktreeBlockedPaths(t *testing.T) {
	t.Run("git_marker_blocked", func(t *testing.T) {
		base := t.TempDir()
		root := filepath.Join(base, "job", "j1")
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, ".git"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		wt, err := AllocWorktree(base, "j1", true)
		if wt != nil || err == nil {
			t.Fatalf("AllocWorktree with a .git file = %+v, %v; want error", wt, err)
		}
	})
	t.Run("read_only_root_blocked", func(t *testing.T) {
		base := t.TempDir()
		if err := os.WriteFile(filepath.Join(base, "ro-j2"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		wt, err := AllocWorktree(base, "j2", false)
		if wt != nil || err == nil {
			t.Fatalf("AllocWorktree read-only over a file = %+v, %v; want error", wt, err)
		}
	})
	t.Run("release_nil_and_empty", func(t *testing.T) {
		var nilWT *Worktree
		if err := nilWT.Release(); err != nil {
			t.Fatalf("nil Release = %v; want nil", err)
		}
		if err := (&Worktree{}).Release(); err != nil {
			t.Fatalf("empty-root Release = %v; want nil", err)
		}
	})
	t.Run("release_invalid_path", func(t *testing.T) {
		if err := (&Worktree{Root: "bad\x00root"}).Release(); err == nil {
			t.Fatal("Release of a path with a NUL byte = nil; want error")
		}
	})
}
