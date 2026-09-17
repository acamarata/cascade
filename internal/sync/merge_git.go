package sync

import (
	"context"
	"strings"
)

// Purpose (this file): phase-state carriage — fetch, then fast-forward,
//
//	and nothing else.
//
// THE ENGINE NEVER MERGES PHASE STATE (R-21.223). Phase state is
//
//	git-tracked files with a meaning this package does not understand: a
//	three-way merge of two ticket trees can produce a tree that is valid
//	YAML and describes a phase nobody planned. So a divergence is REFUSED,
//	journaled with both refs, and surfaced through `sync conflicts list`
//	for a person to resolve in the repository, where the tools for it are.
//
// REAL GIT DECIDES (Art.2). Fast-forward eligibility is git's question and
//
//	git answers it; nothing here reimplements ancestry. The runner is a
//	seam so a test can drive failure paths, and the tests that matter drive
//	the real binary against real repositories.
//
// Inputs: a repository, a remote and a ref.
// Outputs: the ref it fast-forwarded to, or a typed divergence.
// SPORT: internal/sync git carriage (ADD) — P1-E17-W4-S38-T2.

// GitRunner runs one git command in a directory. It is the external
// contract's seam: internal/sync may not spawn processes directly (the
// process-spawn allowlist), and the production runner is injected.
type GitRunner interface {
	Run(ctx context.Context, dir string, args ...string) (string, error)
}

// CarryResult reports one phase-state carriage.
type CarryResult struct {
	// FastForwarded is true when the local ref moved.
	FastForwarded bool
	// LocalRef and RemoteRef are the two commits. They are reported on
	// SUCCESS as well as on divergence: an operator reconstructing what a
	// sync did needs where it came from, not only where it ended up.
	LocalRef  string
	RemoteRef string
}

// CarryPhaseState fetches remoteRef from remote into repo and fast-forwards
// onto it, refusing anything else.
func CarryPhaseState(
	ctx context.Context, git GitRunner, journal *ConflictJournal, dc DomainClass,
	repo, remote, remoteRef string,
) (CarryResult, error) {
	if err := pointRemote(ctx, git, repo, remote); err != nil {
		return CarryResult{}, err
	}
	if out, err := git.Run(ctx, repo, "fetch", carriageRemote, remoteRef); err != nil {
		return CarryResult{}, ErrPhaseStateDivergedf("", remoteRef, "fetch failed: "+firstLine(out)+": "+err.Error())
	}
	local, err := revParse(ctx, git, repo, "HEAD")
	if err != nil {
		return CarryResult{}, err
	}
	remoteHead, err := revParse(ctx, git, repo, "FETCH_HEAD")
	if err != nil {
		return CarryResult{}, err
	}

	if out, err := git.Run(ctx, repo, "merge", "--ff-only", "FETCH_HEAD"); err != nil {
		diverged := ErrPhaseStateDivergedf(local, remoteHead, firstLine(out))
		journal.Record(Conflict{
			Domain: string(dc.Domain), Subkind: dc.Subkind,
			Strategy: StrategyGitCarried, RecordID: remoteRef,
			Winner:     Side{Ref: local},
			Loser:      Side{Ref: remoteHead},
			Resolution: ResolutionRefused,
			Detail: "the two histories diverged; phase state is never engine-merged, " +
				"resolve it in the repository",
		})
		return CarryResult{LocalRef: local, RemoteRef: remoteHead}, diverged
	}
	return CarryResult{FastForwarded: true, LocalRef: local, RemoteRef: remoteHead}, nil
}

// carriageRemote is the remote name this package manages. Fixed, and
// namespaced, so carriage never re-points an operator's own "origin".
const carriageRemote = "cascade-sync"

// pointRemote makes carriageRemote address remote, adding it or re-pointing
// an existing one. Re-pointing rather than failing: a second carriage
// against a different peer is ordinary, and a stale URL is how it would
// silently fetch from the wrong place.
func pointRemote(ctx context.Context, git GitRunner, repo, remote string) error {
	if _, err := git.Run(ctx, repo, "remote", "add", carriageRemote, remote); err == nil {
		return nil
	}
	if out, err := git.Run(ctx, repo, "remote", "set-url", carriageRemote, remote); err != nil {
		return ErrPhaseStateDivergedf("", "", "cannot address the remote: "+firstLine(out)+": "+err.Error())
	}
	return nil
}

// revParse resolves one ref.
func revParse(ctx context.Context, git GitRunner, repo, ref string) (string, error) {
	out, err := git.Run(ctx, repo, "rev-parse", ref)
	if err != nil {
		return "", ErrPhaseStateDivergedf(ref, "", "cannot resolve "+ref+": "+firstLine(out))
	}
	return strings.TrimSpace(out), nil
}

// firstLine trims git's output to its first line, which is the part worth
// carrying into an error. Git's full output on a refused merge is several
// lines of advice aimed at a person at a terminal.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
