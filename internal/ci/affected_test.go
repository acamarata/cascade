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
// commit's hash -- ChangedPathsTree/currentTreeHash tests drive the REAL
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

// treeOf returns the tree object of rev in root, read from the object store.
func treeOf(t *testing.T, root, rev string) string {
	t.Helper()
	return gitOutput(t, root, "rev-parse", rev+"^{tree}")
}

// TestChangedPathsTree_RealGitDiffSubprocess proves ChangedPathsTree reports
// a real committed change via a real `git diff-tree` subprocess.
func TestChangedPathsTree_RealGitDiffSubprocess(t *testing.T) {
	root, base := newGitRepo(t, "a.txt", "one")
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("two"), 0o644); err != nil {
		t.Fatalf("rewriting a.txt: %v", err)
	}
	runGit(t, root, "commit", "-q", "-a", "-m", "second")

	changed, err := ChangedPathsTree(context.Background(), root, base, treeOf(t, root, "HEAD"))
	if err != nil {
		t.Fatalf("ChangedPathsTree: %v", err)
	}
	if len(changed) != 1 || changed[0] != "a.txt" {
		t.Fatalf("ChangedPathsTree = %v, want [a.txt]", changed)
	}
}

// TestChangedPathsTree_NoChangesReturnsEmptyNotNil proves a no-op diff
// (base's tree == tree) returns []string{}, not nil.
func TestChangedPathsTree_NoChangesReturnsEmptyNotNil(t *testing.T) {
	root, base := newGitRepo(t, "a.txt", "one")
	changed, err := ChangedPathsTree(context.Background(), root, base, treeOf(t, root, base))
	if err != nil {
		t.Fatalf("ChangedPathsTree: %v", err)
	}
	if changed == nil {
		t.Fatal("ChangedPathsTree returned nil, want []string{}")
	}
	if len(changed) != 0 {
		t.Fatalf("ChangedPathsTree = %v, want empty", changed)
	}
}

// TestChangedPathsTree_IgnoresLaterCommitsAndStagedEdits proves the answer
// is bound to the given tree: a commit made afterwards that reverts the
// change, and an edit staged afterwards, do not alter it. The two positive
// controls show the same repository does report those states when asked.
func TestChangedPathsTree_IgnoresLaterCommitsAndStagedEdits(t *testing.T) {
	root, base := newGitRepo(t, "a.txt", "one")
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("two"), 0o644); err != nil {
		t.Fatalf("rewriting a.txt: %v", err)
	}
	runGit(t, root, "commit", "-q", "-a", "-m", "second")
	captured := treeOf(t, root, "HEAD")
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("one"), 0o644); err != nil {
		t.Fatalf("reverting a.txt: %v", err)
	}
	runGit(t, root, "commit", "-q", "-a", "-m", "revert")
	if err := os.WriteFile(filepath.Join(root, "staged.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("staged.txt: %v", err)
	}
	runGit(t, root, "add", "staged.txt")

	got, err := ChangedPathsTree(context.Background(), root, base, captured)
	if err != nil || len(got) != 1 || got[0] != "a.txt" {
		t.Fatalf("ChangedPathsTree(captured) = %v, %v; want [a.txt]", got, err)
	}
	head, err := ChangedPathsTree(context.Background(), root, base, treeOf(t, root, "HEAD"))
	if err != nil || len(head) != 0 {
		t.Fatalf("positive control: the reverted HEAD tree must differ from the captured one; got %v, %v", head, err)
	}
	index := gitOutput(t, root, "write-tree")
	staged, err := ChangedPathsTree(context.Background(), root, base, index)
	if err != nil || len(staged) != 1 || staged[0] != "staged.txt" {
		t.Fatalf("positive control: the index tree must show the staged path; got %v, %v", staged, err)
	}
}

// TestChangedPathsTree_MissingBaseCommit proves a blank baseCommit is
// refused before any subprocess runs.
func TestChangedPathsTree_MissingBaseCommit(t *testing.T) {
	root, first := newGitRepo(t, "a.txt", "one")
	_, err := ChangedPathsTree(context.Background(), root, "   ", treeOf(t, root, first))
	if !errors.Is(err, ErrBaseCommitUnknown) {
		t.Fatalf("ChangedPathsTree(blank base) error = %v, want ErrBaseCommitUnknown", err)
	}
}

// TestChangedPathsTree_UnsafeInputsRejectedBeforeSubprocess proves a
// baseCommit carrying shell-unsafe characters or a leading dash, and a tree
// that is not an object id, are refused by the argv allowlist rather than
// ever reaching exec.Command.
func TestChangedPathsTree_UnsafeInputsRejectedBeforeSubprocess(t *testing.T) {
	root, first := newGitRepo(t, "a.txt", "one")
	tree := treeOf(t, root, first)
	for _, base := range []string{"abc; rm -rf /", "--output=/tmp/x"} {
		if _, err := ChangedPathsTree(context.Background(), root, base, tree); !errors.Is(err, ErrBaseCommitUnknown) {
			t.Fatalf("ChangedPathsTree(base %q) error = %v, want ErrBaseCommitUnknown", base, err)
		}
	}
	if _, err := ChangedPathsTree(context.Background(), root, first, "HEAD; true"); !errors.Is(err, ErrBaseCommitUnknown) {
		t.Fatalf("ChangedPathsTree(unsafe tree) error = %v, want ErrBaseCommitUnknown", err)
	}
}

// TestChangedPathsTree_UnknownRevisionRejectedByGit proves a syntactically
// safe but nonexistent baseCommit is still refused as ErrBaseCommitUnknown
// -- git's own real refusal, not a hand-authored check.
func TestChangedPathsTree_UnknownRevisionRejectedByGit(t *testing.T) {
	root, first := newGitRepo(t, "a.txt", "one")
	_, err := ChangedPathsTree(context.Background(), root, "0000000000000000000000000000000000000000", treeOf(t, root, first))
	if !errors.Is(err, ErrBaseCommitUnknown) {
		t.Fatalf("ChangedPathsTree(unknown revision) error = %v, want ErrBaseCommitUnknown", err)
	}
}
