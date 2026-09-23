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

// TestAffectedTargets_NoChangesReturnsEmptyNotNil proves the empty
// changed-path input returns []Target{}, never a nil slice, regardless
// of stack.
func TestAffectedTargets_NoChangesReturnsEmptyNotNil(t *testing.T) {
	targets, err := affectedTargets(context.Background(), "", "go", Config{}, nil)
	if err != nil {
		t.Fatalf("affectedTargets(nil changed) error: %v", err)
	}
	if targets == nil {
		t.Fatal("affectedTargets(nil changed) returned a nil slice, want []Target{}")
	}
	if len(targets) != 0 {
		t.Fatalf("affectedTargets(nil changed) = %v, want empty", targets)
	}
}

// TestAffectedTargets_UnknownStackReturnsFullSet proves an unrecognised
// stack with no affected_cmd configured returns the TargetAll fallback,
// not an error.
func TestAffectedTargets_UnknownStackReturnsFullSet(t *testing.T) {
	targets, err := affectedTargets(context.Background(), "/does/not/matter", "rust", Config{}, []string{"src/lib.rs"})
	if err != nil {
		t.Fatalf("affectedTargets(unknown stack) error: %v", err)
	}
	if len(targets) != 1 || targets[0] != TargetAll {
		t.Fatalf("affectedTargets(unknown stack) = %v, want [%s]", targets, TargetAll)
	}
}

// newGitRepo creates a real git repository in t.TempDir(), commits
// initialContent under name, and returns the repo root and that first
// commit's hash -- ChangedPaths/currentTreeHash tests drive the REAL
// git binary against it (Art.2: no test double for the git path either).
func newGitRepo(t *testing.T, name, initialContent string) (root, firstCommit string) {
	t.Helper()
	root = t.TempDir()
	runGit(t, root, "init", "-q")
	runGit(t, root, "config", "user.email", "test@example.com")
	runGit(t, root, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(root, name), []byte(initialContent), 0o644); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}
	runGit(t, root, "add", name)
	runGit(t, root, "commit", "-q", "-m", "initial")
	firstCommit = gitOutput(t, root, "rev-parse", "HEAD")
	return root, firstCommit
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// gitOutput runs git in dir and returns its trimmed stdout, failing the
// test on any error.
func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

// TestChangedPaths_RealGitDiffSubprocess proves ChangedPaths reports a
// real committed change via a real `git diff --name-only` subprocess.
func TestChangedPaths_RealGitDiffSubprocess(t *testing.T) {
	root, base := newGitRepo(t, "a.txt", "one")
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("two"), 0o644); err != nil {
		t.Fatalf("rewriting a.txt: %v", err)
	}
	runGit(t, root, "commit", "-q", "-a", "-m", "second")

	changed, err := ChangedPaths(context.Background(), root, base)
	if err != nil {
		t.Fatalf("ChangedPaths: %v", err)
	}
	if len(changed) != 1 || changed[0] != "a.txt" {
		t.Fatalf("ChangedPaths = %v, want [a.txt]", changed)
	}
}

// TestChangedPaths_NoChangesReturnsEmptyNotNil proves a no-op diff
// (base == HEAD) returns []string{}, not nil.
func TestChangedPaths_NoChangesReturnsEmptyNotNil(t *testing.T) {
	root, base := newGitRepo(t, "a.txt", "one")
	changed, err := ChangedPaths(context.Background(), root, base)
	if err != nil {
		t.Fatalf("ChangedPaths: %v", err)
	}
	if changed == nil {
		t.Fatal("ChangedPaths returned nil, want []string{}")
	}
	if len(changed) != 0 {
		t.Fatalf("ChangedPaths = %v, want empty", changed)
	}
}

// TestChangedPaths_MissingBaseCommit proves a blank baseCommit is
// refused before any subprocess runs.
func TestChangedPaths_MissingBaseCommit(t *testing.T) {
	root, _ := newGitRepo(t, "a.txt", "one")
	_, err := ChangedPaths(context.Background(), root, "   ")
	if !errors.Is(err, ErrBaseCommitUnknown) {
		t.Fatalf("ChangedPaths(blank base) error = %v, want ErrBaseCommitUnknown", err)
	}
}

// TestChangedPaths_UnsafeBaseCommitRejectedBeforeSubprocess proves a
// baseCommit carrying shell-unsafe characters is refused by the argv
// allowlist rather than ever reaching exec.Command.
func TestChangedPaths_UnsafeBaseCommitRejectedBeforeSubprocess(t *testing.T) {
	root, _ := newGitRepo(t, "a.txt", "one")
	_, err := ChangedPaths(context.Background(), root, "abc; rm -rf /")
	if !errors.Is(err, ErrBaseCommitUnknown) {
		t.Fatalf("ChangedPaths(unsafe base) error = %v, want ErrBaseCommitUnknown", err)
	}
}

// TestChangedPaths_UnknownRevisionRejectedByGit proves a syntactically
// safe but nonexistent baseCommit is still refused as ErrBaseCommitUnknown
// -- git's own real refusal, not a hand-authored check.
func TestChangedPaths_UnknownRevisionRejectedByGit(t *testing.T) {
	root, _ := newGitRepo(t, "a.txt", "one")
	_, err := ChangedPaths(context.Background(), root, "0000000000000000000000000000000000000000")
	if !errors.Is(err, ErrBaseCommitUnknown) {
		t.Fatalf("ChangedPaths(unknown revision) error = %v, want ErrBaseCommitUnknown", err)
	}
}
