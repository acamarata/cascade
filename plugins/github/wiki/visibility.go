// Purpose (this file): the repository-visibility seam sync.go and drift.go
//
//	both check before touching a wiki git endpoint. SCOPE (ticket
//	full_desc): "public repos only. Private repos do not expose a GitHub
//	wiki git endpoint under standard OAuth scopes; the sync command
//	refuses gracefully on private repos with an actionable error."
//
// Inputs: an owner/repo pair.
// Outputs: whether the repository is private, or a typed error when the
//
//	check itself cannot be answered (token missing, repo not found).
//
// Constraints: this package may not import internal/** (Art.10.2); the
//
//	production VisibilityChecker is an adapter over plugins/github/tools'
//	existing Client — this package never opens its own HTTP connection.
//
// SPORT: plugins/github/wiki:visibility (ADD) — P1-E25-W5-S51-T6.

package wiki

import (
	"context"

	"github.com/acamarata/cascade/pkg/cascade"
)

// VisibilityChecker reports whether owner/repo is a private repository.
// The production implementation (main.go's wiki dispatch) wraps the
// broker's existing tools.Client — the same "repos.get" call
// repos.get/repos.clone_url already make — so no new API surface or net
// scope is introduced by this check; only github.com (git protocol,
// resolveWikiURL) is new.
type VisibilityChecker interface {
	IsPrivate(ctx context.Context, owner, repo string) (bool, error)
}

// requireNotPrivate refuses with an actionable, typed error when checker
// reports the repository private, or when the check itself fails (fail
// closed: an unanswerable visibility check must not be treated as
// "public").
func requireNotPrivate(ctx context.Context, checker VisibilityChecker, owner, repo string) error {
	if checker == nil {
		return cascade.New(cascade.KindInternal, "cascade-github wiki: no visibility checker configured")
	}
	private, err := checker.IsPrivate(ctx, owner, repo)
	if err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err,
			"cascade-github wiki: could not determine whether %s/%s is private", owner, repo)
	}
	if private {
		return cascade.Newf(cascade.KindPolicyDenied,
			"cascade-github wiki: %s/%s is private; GitHub does not expose a wiki git endpoint for a "+
				"private repository under standard OAuth scopes — wiki sync and wiki check are public-repos-only",
			owner, repo)
	}
	return nil
}
