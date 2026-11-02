package ci

// Purpose: enumerate local branch tips without trusting remote-tracking refs.
// Inputs: a repository root and a bounded command context.
// Outputs: sorted full branch refs and their object ids.
// Constraints: ambient worktree overrides are stripped from the child process.
// SPORT: local branch registration facts.

import (
	"context"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/pkg/cascade"
)

// LocalBranchTips reads only local branch refs, including from linked worktrees.
func LocalBranchTips(ctx context.Context, root string) ([]jobs.BranchTip, error) {
	out, err := baselineGit(ctx, root, "for-each-ref", "--format=%(refname)%00%(objectname)", "refs/heads/")
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "ci: read local branch tips")
	}
	tips := make([]jobs.BranchTip, 0)
	for _, line := range strings.Split(strings.TrimSuffix(string(out), "\n"), "\n") {
		if line == "" {
			continue
		}
		ref, commit, ok := strings.Cut(line, "\x00")
		if !ok || !strings.HasPrefix(ref, "refs/heads/") || !branchObjectID(commit) {
			return nil, cascade.New(cascade.KindIntegrity, "ci: malformed local branch tip")
		}
		tips = append(tips, jobs.BranchTip{Ref: ref, Commit: commit})
	}
	sort.Slice(tips, func(i, j int) bool { return tips[i].Ref < tips[j].Ref })
	return tips, nil
}

func branchObjectID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func baselineGit(ctx context.Context, root string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = root
	for _, v := range os.Environ() {
		if !strings.HasPrefix(v, "GIT_DIR=") && !strings.HasPrefix(v, "GIT_WORK_TREE=") {
			cmd.Env = append(cmd.Env, v)
		}
	}
	return cmd.Output()
}
