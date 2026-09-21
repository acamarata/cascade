// Purpose (this file): the PLUGIN-HOST CALL PATH bridge `cascade github
// wiki sync`/`cascade github wiki check` dispatch cascade-github.wiki.sync
// and cascade-github.wiki.check through (P1-E25-W5-S51-T6, D1), mirroring
// ci_waitmerge_wiring.go's NewGitHubMergeCallerForHost (S-51.T3) almost
// exactly: the SAME cascadeGitHubPluginID and HandleProvider this file's
// sibling already declares, and the SAME honest default -- no code path
// in this tree can produce a live process.Handle for cascade-github today
// (see that file's header comment for why: every process-tier install
// evaluates through process.ProcessRuntime.Launch's real trust gate,
// which always refuses process.TrustTierUntrusted). A future ticket that
// adds trust elevation and a live-Handle registry replaces
// noRunningPluginHandle without changing GitHubPluginCaller's shape.
//
// Inputs: none at import time. Each call resolves the running
// cascade-github process's Handle through the injected HandleProvider.
//
// Outputs: a GitHubPluginCaller cmd/cascade/github_wiki_cmd.go dispatches
// wiki.sync/wiki.check through, or the same typed unmet-prerequisite
// refusal NewGitHubMergeCallerForHost returns for prs.merge.
//
// SPORT: internal/plugins wiki-wiring/ADDED (P1-E25-W5-S51-T6).

package plugins

import (
	"context"

	"github.com/acamarata/cascade/internal/plugins/process"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Compile-time proof that *process.Handle satisfies GitHubPluginCaller
// with NO adapter code, the identical shape ci_waitmerge_wiring.go proves
// for ci.MergeCaller (both interfaces are the same one method).
var _ GitHubPluginCaller = (*process.Handle)(nil)

// GitHubPluginCaller is the minimal transport cmd/cascade's wiki verbs
// need: one RPC method call against a live cascade-github process.
type GitHubPluginCaller interface {
	Call(ctx context.Context, method string, params []byte) ([]byte, error)
}

// NewGitHubWikiCaller returns a GitHubPluginCaller that resolves
// cascade-github's live Handle through provider on every call and
// dispatches through its real stdio transport. Passing a nil provider
// makes every call refuse rather than panic.
func NewGitHubWikiCaller(provider HandleProvider) GitHubPluginCaller {
	return &githubWikiCaller{provider: provider}
}

type githubWikiCaller struct {
	provider HandleProvider
}

// Call implements GitHubPluginCaller.
func (c *githubWikiCaller) Call(ctx context.Context, method string, params []byte) ([]byte, error) {
	if c.provider == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "plugins: the github wiki caller has no HandleProvider")
	}
	h, err := c.provider(ctx, cascadeGitHubPluginID)
	if err != nil {
		return nil, err
	}
	if h == nil {
		return nil, cascade.New(cascade.KindInternal, "plugins: the HandleProvider returned no Handle and no error")
	}
	return h.Call(ctx, method, params)
}

// wikiHandleProvider is the HandleProvider NewGitHubWikiCallerForHost
// consults, for both the prerequisite check and the caller it
// constructs on success.
//
// Purpose: an injectable seam so a test can prove ForHost's SUCCESS
// branch -- constructing a GitHubPluginCaller and returning a nil error
// -- actually runs, without a live cascade-github process. Before this
// var existed, ForHost called noRunningPluginHandle directly, and since
// that function always errors in this build (see this file's header
// comment), the success `return ..., nil` line was unreachable by any
// test and by production alike.
// Inputs: none at import time; test-only reassignment thereafter.
// Outputs: the HandleProvider both branches of ForHost call.
// Constraints: package-scope, not per-call -- a test that reassigns it
// must restore the original value (t.Cleanup) and must not run
// t.Parallel with another test that also reassigns it. The real binary
// never reassigns this var: production stays on noRunningPluginHandle's
// honest refusal until a future ticket adds a real trust-elevation
// provider, at which point this var's default -- not a second code
// path -- is what changes.
// SPORT: internal/plugins wiki-wiring/ADDED (P1-E25-W5-S51-T6).
var wikiHandleProvider HandleProvider = noRunningPluginHandle

// NewGitHubWikiCallerForHost is the PRODUCTION entry point
// cmd/cascade/github_wiki_cmd.go builds `cascade github wiki
// sync`/`check`'s transport with (P1-E25-W5-S51-T6, D1). It returns
// either a working GitHubPluginCaller or the unmet prerequisite that
// makes wiki sync/check impossible today, as a typed error naming that
// prerequisite -- never a caller that would refuse later with a less
// specific message, and never a fabricated success.
//
// Today it always returns the refusal, for the identical reason
// NewGitHubMergeCallerForHost does (see this file's header comment): the
// trust-elevation mechanism a process-tier Launch needs does not exist.
func NewGitHubWikiCallerForHost(ctx context.Context) (GitHubPluginCaller, error) {
	if _, err := wikiHandleProvider(ctx, cascadeGitHubPluginID); err != nil {
		return nil, cascade.Wrapf(cascade.KindUnavailable, err,
			"plugins: `cascade github wiki sync`/`wiki check` need a live %s process to dispatch "+
				"wiki.sync/wiki.check through, and this build has no trust-elevation path that can launch "+
				"one. The elevated `plugin add` install flow that can mark a process-tier manifest trusted "+
				"is P1-E24-W5-S50-T4's; until it lands, no process-tier plugin starts", cascadeGitHubPluginID)
	}
	return NewGitHubWikiCaller(wikiHandleProvider), nil
}
