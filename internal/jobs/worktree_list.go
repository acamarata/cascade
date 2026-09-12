package jobs

// Purpose: HOW step 6 — a strict parser over `git worktree list
//
//	--porcelain` output, so the sweep (worktree_sweep.go) compares stored
//	rows against what actually exists on disk without ever guessing at
//	an entry it cannot parse. FuzzWorktreePorcelain (worktree_list_test.go)
//	proves 30s of adversarial input never panics and always resolves to
//	either a well-formed []WorktreeEntry or a typed parse error.
//
// Inputs: the raw stdout bytes of `git worktree list --porcelain`.
// Outputs: []WorktreeEntry in listed order, or a cascade.KindInvalidInput
//
//	error naming the offending line — never a partially-guessed entry.
//
// Constraints: the porcelain format's grammar (git-worktree(1)): entries
//
//	are blank-line-separated blocks, each starting with a "worktree
//	<path>" line; recognized attribute lines are HEAD, branch, bare,
//	detached, locked[ <reason>], prunable[ <reason>]. Any other line, or
//	an attribute line before the block's own "worktree " header, is
//	fail-closed malformed input.
//
// SPORT: jobs/worktree-manager (ADD, P1-E29-W6-S59-T3).

import (
	"context"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// WorktreeEntry is one parsed `git worktree list --porcelain` block.
type WorktreeEntry struct {
	Path           string
	HEAD           string
	Branch         string // ref short name (refs/heads/ stripped); empty if Detached
	Bare           bool
	Detached       bool
	Locked         bool
	LockedReason   string
	Prunable       bool
	PrunableReason string
}

// ParseWorktreePorcelain parses `git worktree list --porcelain` stdout
// into its listed entries. Fail-closed: any line this grammar does not
// recognize, or an attribute line appearing before its block's "worktree "
// header, is a typed error naming the offending line — never a guessed or
// partially-populated entry.
func ParseWorktreePorcelain(data []byte) ([]WorktreeEntry, error) {
	var entries []WorktreeEntry
	var cur *WorktreeEntry

	flush := func() error {
		if cur == nil {
			return nil
		}
		if cur.Path == "" {
			return cascade.New(cascade.KindInvalidInput, "jobs: worktree porcelain entry has an empty worktree path")
		}
		entries = append(entries, *cur)
		cur = nil
		return nil
	}

	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			if err := flush(); err != nil {
				return nil, err
			}
			continue
		}
		if strings.HasPrefix(line, "worktree ") {
			if err := flush(); err != nil {
				return nil, err
			}
			cur = &WorktreeEntry{Path: strings.TrimPrefix(line, "worktree ")}
			continue
		}
		if cur == nil {
			return nil, cascade.Newf(cascade.KindInvalidInput, "jobs: worktree porcelain line %q precedes any worktree header", line)
		}
		if err := applyPorcelainAttr(cur, line); err != nil {
			return nil, err
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return entries, nil
}

// applyPorcelainAttr applies one recognized attribute line to cur, or
// returns a typed parse error for an unrecognized line.
func applyPorcelainAttr(cur *WorktreeEntry, line string) error {
	switch {
	case strings.HasPrefix(line, "HEAD "):
		cur.HEAD = strings.TrimPrefix(line, "HEAD ")
	case strings.HasPrefix(line, "branch "):
		cur.Branch = strings.TrimPrefix(strings.TrimPrefix(line, "branch "), "refs/heads/")
	case line == "bare":
		cur.Bare = true
	case line == "detached":
		cur.Detached = true
	case line == "locked":
		cur.Locked = true
	case strings.HasPrefix(line, "locked "):
		cur.Locked = true
		cur.LockedReason = strings.TrimPrefix(line, "locked ")
	case line == "prunable":
		cur.Prunable = true
	case strings.HasPrefix(line, "prunable "):
		cur.Prunable = true
		cur.PrunableReason = strings.TrimPrefix(line, "prunable ")
	default:
		return cascade.Newf(cascade.KindInvalidInput, "jobs: unrecognized worktree porcelain line %q", line)
	}
	return nil
}

// listGitWorktrees runs `git worktree list --porcelain` in repoRoot and
// parses its output — the sweep's on-disk truth source.
func (m *WorktreeManager) listGitWorktrees(ctx context.Context, repoRoot string) ([]WorktreeEntry, error) {
	out, err := m.git.run(ctx, repoRoot, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	return ParseWorktreePorcelain([]byte(out))
}
