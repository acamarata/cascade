package repo

// Purpose: read a repository's change state from the real git binary: the
//   porcelain status, the diff since the journaled commit and the HEAD tree
//   OID (contract:repo-change-feed).
// Inputs: a repository root and the commit the watch cursor last recorded.
// Outputs: GitState (HEAD, tree OID, sorted slash repo-relative paths), or
//   ErrGap when the cursor commit is not an ancestor of HEAD.
// Constraints: only `git status --porcelain=v1 -z`, `git merge-base
//   --is-ancestor`, `git diff --name-only -z` and `git rev-parse`; no
//   self-written status format beyond splitting the -z records.
// SPORT: repo/change-feed/ADD (contract:repo-change-feed, P1-GEN-14).

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"sort"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// ErrGap is returned when the watch cursor's commit is not an ancestor of
// HEAD, so the cursor cannot advance monotonically and a sweep must run.
var ErrGap = cascade.New(cascade.KindConflict, "repo: watch cursor cannot advance monotonically")

// GitState is one reading of a repository's git state.
type GitState struct {
	Head, TreeOID string
	Changed       []string
}

// ReadGitState reports the staged, unstaged and untracked paths, the paths
// changed by commits since sinceCommit (when it is an ancestor of HEAD) and
// the HEAD tree OID. A sinceCommit that is not an ancestor yields ErrGap.
func ReadGitState(ctx context.Context, root, sinceCommit string) (GitState, error) {
	var gs GitState
	status, err := gitOut(ctx, root, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return gs, err
	}
	changed := porcelainPaths(status)
	head, err := gitOut(ctx, root, "rev-parse", "-q", "--verify", "HEAD^{commit}")
	if err == nil {
		gs.Head = strings.TrimSpace(string(head))
	}
	if gs.Head != "" {
		tree, terr := gitOut(ctx, root, "rev-parse", "HEAD^{tree}")
		if terr != nil {
			return gs, terr
		}
		gs.TreeOID = strings.TrimSpace(string(tree))
	}
	if sinceCommit != "" && sinceCommit != gs.Head {
		diff, derr := diffSince(ctx, root, sinceCommit, gs.Head)
		if derr != nil {
			return gs, derr
		}
		changed = append(changed, diff...)
	}
	gs.Changed = normalizeRels(changed)
	return gs, nil
}

// diffSince lists the paths changed between since and head, or ErrGap when
// since is not an ancestor of head (including an unknown commit or no HEAD).
func diffSince(ctx context.Context, root, since, head string) ([]string, error) {
	if head == "" {
		return nil, ErrGap
	}
	cmd := exec.CommandContext(ctx, "git", "-C", root, "merge-base", "--is-ancestor", since, head)
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if ctx.Err() == nil && errors.As(err, &ee) {
			return nil, ErrGap
		}
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "repo: git merge-base failed")
	}
	out, err := gitOut(ctx, root, "diff", "--name-only", "--no-renames", "-z", since+".."+head)
	if err != nil {
		return nil, err
	}
	return splitZ(out), nil
}

// porcelainPaths extracts every path of `git status --porcelain=v1 -z`
// records "XY path"; a rename or copy carries its source as the next record.
func porcelainPaths(out []byte) []string {
	recs := splitZ(out)
	var paths []string
	for i := 0; i < len(recs); i++ {
		r := recs[i]
		if len(r) < 4 {
			continue
		}
		paths = append(paths, r[3:])
		if r[0] == 'R' || r[0] == 'C' {
			if i+1 < len(recs) {
				paths = append(paths, recs[i+1])
			}
			i++
		}
	}
	return paths
}

// lsFiles lists every tracked path (`git ls-files -z`), sorted.
func lsFiles(ctx context.Context, root string) ([]string, error) {
	out, err := gitOut(ctx, root, "ls-files", "-z")
	if err != nil {
		return nil, err
	}
	files := splitZ(out)
	sort.Strings(files)
	return files, nil
}

// splitZ splits NUL-terminated records, dropping empties.
func splitZ(out []byte) []string {
	var recs []string
	for _, r := range bytes.Split(out, []byte{0}) {
		if len(r) > 0 {
			recs = append(recs, string(r))
		}
	}
	return recs
}

// gitOut runs git in root and returns stdout; a failure is KindUnavailable.
func gitOut(ctx context.Context, root string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", root}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, cascade.Wrapf(cascade.KindUnavailable, err, "repo: git %s failed: %s",
			args[0], strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}
