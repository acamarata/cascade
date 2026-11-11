package delegation

// Purpose (this file): worktree allocation for delegation.
// Mutation jobs get a minimal git checkout scoped to job/<job id>;
// read-only jobs get an empty 0700 directory. Worktrees are released
// before the lease is granted.
//
// SPORT: delegation.worktree (ADD, P1-AGT-08).

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Worktree is a delegation worktree.
type Worktree struct {
	// Root is the absolute path to the worktree root.
	Root string
	// GitCwd is the git working directory for mutation jobs; empty for read-only.
	GitCwd string
	// ReadOnly is true when the worktree is an empty read-only directory.
	ReadOnly bool
}

// AllocWorktree allocates a worktree under base for the given job.
// For mutation jobs, it creates job/<jobID> with a .git marker.
// For read-only jobs, it creates an empty 0700 directory.
func AllocWorktree(base string, jobID string, mutation bool) (*Worktree, error) {
	if jobID == "" || jobID == "." || jobID == ".." || filepath.IsAbs(jobID) || strings.ContainsAny(jobID, `/\`) {
		return nil, cascade.New(cascade.KindInvalidInput, "worktree: job_id must be a nonempty path component")
	}
	root := filepath.Join(base, "ro-"+jobID)
	if mutation {
		root = filepath.Join(base, "job", jobID)
	}
	// Verify containment before creating any part of the worktree.
	rel, err := filepath.Rel(base, root)
	if err != nil || rel == "." || !filepath.IsLocal(rel) {
		return nil, cascade.New(cascade.KindInvalidInput, "worktree: root must stay under base")
	}
	if mutation {
		if err := os.MkdirAll(root, 0o755); err != nil {
			return nil, err
		}
		// Minimal git marker so git commands recognize this as a repo.
		gitDir := filepath.Join(root, ".git")
		if err := os.MkdirAll(gitDir, 0o755); err != nil {
			return nil, err
		}
		return &Worktree{Root: root, GitCwd: root, ReadOnly: false}, nil
	}
	// Read-only: empty 0700 directory.
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	return &Worktree{Root: root, GitCwd: "", ReadOnly: true}, nil
}

// Release removes the worktree from disk.
func (w *Worktree) Release() error {
	if w == nil || w.Root == "" {
		return nil
	}
	return os.RemoveAll(w.Root)
}
