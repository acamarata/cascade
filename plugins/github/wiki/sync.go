// Package wiki implements cascade-github's wiki automation: `cascade
// github wiki sync` (push .github/wiki/ to the repository's GitHub wiki)
// and `cascade github wiki check` (drift.go — read-only comparison).
//
// Purpose (this file): Sync — resolve the wiki git URL from a repo slug,
//
//	clone it to an isolated tempdir, overwrite it from the local
//	.github/wiki/ directory, commit and push.
//
// Inputs: SyncOptions (owner, repo, the local wiki directory, whether the
//
//	caller already confirmed the push, the OAuth token, and three
//	injectable seams: GitRunner, VisibilityChecker and Getenv).
//
// Outputs: a SyncResult reporting what happened (no-op and why, or the
//
//	files that changed and that the push succeeded), or a typed error.
//
// Constraints: SCOPE is public repos only (visibility.go). The git
//
//	operations are exec-only invocations of the system git binary (06
//	§5.15: clone is L1-L2, push is L3) — this package never speaks the git
//	wire protocol itself. plugins/** may not import internal/** (Art.10.2):
//	the H/S-16.T1 wiki-git-push egress class this ticket registers in
//	internal/hooks/egress/classes.go documents this outbound path for the
//	central inventory, but — exactly as plugins/github/main.go's own
//	api.github.com calls already are (P1-E25-W5-S51-T1's "declared,
//	accepted-risk design, trusted tier") — this process-tier plugin cannot
//	call the host's egress.Engine.Intercept across the process boundary,
//	so the same trusted-tier consent model (manifest net scope + the
//	permissions display) is this ticket's gate too, not a live
//	Interceptor call. Declared explicitly here rather than left to be
//	rediscovered; see this ticket's journal for the precedent citation.
//
// SPORT: plugins/github/wiki:sync (ADD) — P1-E25-W5-S51-T6.
package wiki

import (
	"context"
	"os"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// SyncOptions configures one Sync call.
type SyncOptions struct {
	// Owner and Repo name the repository whose wiki is being synced.
	Owner, Repo string
	// LocalDir is the local .github/wiki/ directory (or any directory
	// holding the wiki content the caller wants pushed).
	LocalDir string
	// Yes reports whether the caller already obtained confirmation
	// (--yes, or an interactive y/N at the CLI layer, which has the
	// terminal this stdio plugin process does not).
	Yes bool
	// Token authorizes the clone and push. Never logged, never reported
	// in a display URL.
	Token string
	// Runner performs git subprocess calls. Nil uses the real git binary.
	Runner GitRunner
	// Checker reports repository visibility. Required: Sync refuses with
	// KindInternal if nil, the same fail-closed posture requireNotPrivate
	// already documents.
	Checker VisibilityChecker
	// Getenv reads the environment for the CASCADE_NO_INPUT check. Nil
	// uses os.Getenv.
	Getenv func(string) string
	// MkdirTemp creates the isolated clone directory. Nil uses
	// os.MkdirTemp(cache dir, pattern) — tests inject t.TempDir()-backed
	// values so no production temp directory is touched by a test.
	MkdirTemp func() (dir string, cleanup func(), err error)
}

// SyncResult reports what Sync did.
type SyncResult struct {
	// NoOp is true when nothing was pushed — an empty local wiki, or a
	// local wiki that already matches the remote exactly.
	NoOp bool
	// Reason explains NoOp; empty when NoOp is false.
	Reason string
	// Changed lists the paths (relative to LocalDir) that differed from
	// the remote wiki and were pushed, sorted.
	Changed []string
	// Pushed is true once the push itself succeeded.
	Pushed bool
}

// Sync implements `cascade github wiki sync`. The order is fixed and
// matters: the cheapest, purely-local check (an empty local directory)
// runs before anything that reaches the network or asks for
// confirmation, so a no-op costs nothing.
func Sync(ctx context.Context, opts SyncOptions) (SyncResult, error) {
	if err := validateOwnerRepo(opts.Owner, opts.Repo); err != nil {
		return SyncResult{}, err
	}
	local, err := readTree(opts.LocalDir)
	if err != nil {
		return SyncResult{}, err
	}
	if len(local) == 0 {
		return SyncResult{NoOp: true, Reason: "local .github/wiki/ is empty; nothing to sync"}, nil
	}
	if err := requireNotPrivate(ctx, opts.Checker, opts.Owner, opts.Repo); err != nil {
		return SyncResult{}, err
	}
	if err := requireConfirmation(opts.Yes, opts.Getenv); err != nil {
		return SyncResult{}, err
	}
	if err := requireGit(); err != nil {
		return SyncResult{}, err
	}
	return syncPush(ctx, opts)
}

// syncPush runs the clone/overwrite/commit/push sequence, once every
// precondition above has passed. Split from Sync to keep both under the
// 50-line function cap.
func syncPush(ctx context.Context, opts SyncOptions) (SyncResult, error) {
	tmp, cleanup, err := makeTempDir(opts.MkdirTemp)
	if err != nil {
		return SyncResult{}, err
	}
	defer cleanup()

	runner := newRunner(opts.Runner)
	url := resolveWikiURL(opts.Owner, opts.Repo)
	authEnv := gitAuthEnv(opts.Token)
	if err := cloneWiki(ctx, runner, url, tmp, authEnv); err != nil {
		return SyncResult{}, err
	}
	changed, err := overwriteTree(opts.LocalDir, tmp)
	if err != nil {
		return SyncResult{}, err
	}
	if len(changed) == 0 {
		return SyncResult{NoOp: true, Reason: "local .github/wiki/ already matches the remote wiki"}, nil
	}
	if err := commitAll(ctx, runner, tmp); err != nil {
		return SyncResult{}, err
	}
	if err := pushWiki(ctx, runner, tmp, url, authEnv); err != nil {
		return SyncResult{}, err
	}
	return SyncResult{Changed: changed, Pushed: true}, nil
}

// resolveWikiURL builds the wiki git remote from a repo slug. It NEVER
// carries the token (D3, confirming review finding 3): the URL becomes a
// git argv element (`git clone <url> <dir>`), and argv is visible to any
// user on the machine via `ps`. Authentication travels separately, in
// gitAuthEnv's per-invocation environment.
func resolveWikiURL(owner, repo string) string {
	return "https://github.com/" + owner + "/" + repo + ".wiki.git"
}

// validateOwnerRepo refuses an owner or repo value that could address
// something other than exactly one repository (an empty value, or one
// carrying a path separator or a `..` traversal segment) before it ever
// reaches a URL or a filesystem path.
func validateOwnerRepo(owner, repo string) error {
	for field, v := range map[string]string{"owner": owner, "repo": repo} {
		switch {
		case strings.TrimSpace(v) == "":
			return cascade.Newf(cascade.KindInvalidInput, "cascade-github wiki: %s is empty", field)
		case strings.ContainsAny(v, "/\\"), strings.Contains(v, ".."):
			return cascade.Newf(cascade.KindInvalidInput,
				"cascade-github wiki: %s %q contains a path separator; it must name one %s only", field, v, field)
		}
	}
	return nil
}

// makeTempDir returns an isolated directory for the clone, and a cleanup
// function the caller defers. build defaults to a real os.MkdirTemp under
// os.TempDir(); tests inject one backed by t.TempDir() so nothing under
// this package's tests ever touches a real system temp directory beyond
// what the testing package itself manages.
func makeTempDir(build func() (string, func(), error)) (string, func(), error) {
	if build != nil {
		return build()
	}
	dir, err := os.MkdirTemp("", "cascade-github-wiki-*")
	if err != nil {
		return "", func() {}, cascade.Wrap(cascade.KindUnavailable, err, "cascade-github wiki: creating a temp directory")
	}
	return dir, func() { _ = os.RemoveAll(dir) }, nil
}
