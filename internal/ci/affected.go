// Purpose (this file): Affected's dispatch across the Go-stack path
// (affected_go.go), the generic [ci].affected_cmd path (affected_cmd.go),
// and the conservative TargetAll fallback; plus ChangedPaths, the real
// `git diff --name-only` subprocess AC/S-59.T1's execution record feeds
// baseCommit into.
//
// Inputs: worktreeRoot, a stack identifier string ("go" is the one
// recognised value -- anything else, including "", is "unrecognised"),
// a Config (only AffectedCmd matters here), and an already-computed
// changed-path list (ChangedPaths' own output, or a caller-supplied list
// in a test).
// Outputs: the minimal []Target set whose transitive inputs include at
// least one changed path, or []Target{TargetAll} when that cannot be
// computed -- conservative-correct, never a false skip (06 §5.20).
// Constraints: argv-based subprocess only (no shell interpretation),
// matching internal/retrieval/gitcorpus.go's GitTrackedFiles precedent --
// baseCommit and worktreeRoot never pass through a shell string.
// ErrBaseCommitUnknown is a plain sentinel wrapped with a taxonomy Kind
// (not a bare *cascade.Error sentinel): pkg/cascade's own Is method
// compares Kind only, so a plain wrapped sentinel is what lets a caller's
// errors.Is(err, ErrBaseCommitUnknown) mean THIS failure specifically,
// not any KindInvalidInput error (lesson_errors_is_compares_kind_only).
// SPORT: internal.ci.ChangedPaths/ADDED, internal.ci.ErrBaseCommitUnknown/ADDED
//
//	(P1-E32-W6-S65-T1).

package ci

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"regexp"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// stackGo is the one Stack value affectedTargets dispatches to the real
// go list subprocess path. Any other value -- including "" -- falls
// through to Config.AffectedCmd, then the TargetAll fallback.
const stackGo = "go"

// ErrBaseCommitUnknown reports a baseCommit that is blank, whitespace, an
// unsafe value, or one git itself could not resolve against HEAD. Plain
// errors.New sentinel (not a bare *cascade.Error): see this file's own
// Constraints note on why.
var ErrBaseCommitUnknown = errors.New("ci: base commit is missing or invalid")

// baseCommitPattern is the safe-argv allowlist a baseCommit must match
// before it is placed on a subprocess argv at all: git ref/SHA
// characters only. Checked BEFORE the subprocess runs so an obviously
// garbage value is refused deterministically -- not merely relying on
// argv-based exec's freedom from shell interpretation, and not
// depending on git's own error-message shape to classify the input.
var baseCommitPattern = regexp.MustCompile(`^[A-Za-z0-9._/+-]+$`)

// ChangedPaths returns the paths that differ between baseCommit and
// worktreeRoot's current HEAD, via a real `git diff --name-only
// <baseCommit>..HEAD` subprocess run inside worktreeRoot. A missing or
// invalid baseCommit -- blank, unsafe, or a value git refuses to resolve
// -- returns ErrBaseCommitUnknown rather than a raw git failure or a
// panic. The result is never nil: zero changed paths is []string{}.
func ChangedPaths(ctx context.Context, worktreeRoot, baseCommit string) ([]string, error) {
	trimmed := strings.TrimSpace(baseCommit)
	if trimmed == "" || !baseCommitPattern.MatchString(trimmed) {
		return nil, cascade.Wrapf(cascade.KindInvalidInput, ErrBaseCommitUnknown,
			"ci: base commit %q is missing or invalid", baseCommit)
	}
	cmd := exec.CommandContext(ctx, "git", "diff", "--name-only", trimmed+"..HEAD")
	cmd.Dir = worktreeRoot
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, cascade.Wrapf(cascade.KindInvalidInput, errors.Join(ErrBaseCommitUnknown, err),
			"ci: base commit %q could not be diffed against HEAD in %q: %s",
			trimmed, worktreeRoot, strings.TrimSpace(stderr.String()))
	}
	return splitNonEmptyLines(stdout.String()), nil
}

// affectedTargets is Affected's dispatch core. RequirementModel.Affected
// (requirements.go) is a one-line forwarding wrapper so affected_test.go
// can drive this function directly with a bare Config, without
// constructing a full RequirementModel for every table case.
func affectedTargets(ctx context.Context, worktreeRoot, stack string, cfg Config, changed []string) ([]Target, error) {
	if len(changed) == 0 {
		return []Target{}, nil
	}
	if stack == stackGo {
		return affectedGoTargets(ctx, worktreeRoot, changed)
	}
	if cfg.AffectedCmd != "" {
		return affectedCmdTargets(ctx, worktreeRoot, cfg.AffectedCmd, changed)
	}
	// Unrecognised stack, or a non-Go stack with no affected_cmd
	// configured: conservative-correct fallback, never a false skip.
	return []Target{TargetAll}, nil
}

// splitNonEmptyLines splits s on newlines, trims each line, and drops
// blanks. The result is never nil: zero surviving lines is []string{}.
func splitNonEmptyLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	if out == nil {
		out = []string{}
	}
	return out
}
