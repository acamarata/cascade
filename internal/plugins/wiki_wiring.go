// Purpose (this file): the PLUGIN-HOST CALL PATH bridge `cascade github
// wiki sync`/`cascade github wiki check` dispatch cascade-github.wiki.sync
// and cascade-github.wiki.check through (P1-E25-W5-S51-T6, D1), mirroring
// ci_waitmerge_wiring.go: the same cascadeGitHubPluginID, HandleProvider and
// per-call launch (callOnce), over a live, recorded TrustedHandle.
//
// Inputs: none at import time. Each call launches cascade-github through the
// injected HandleProvider (production: InstalledHandleProvider).
//
// Outputs: a GitHubPluginCaller cmd/cascade/github_wiki_cmd.go dispatches
// wiki.sync/wiki.check through, or the typed refusal naming
// `cascade plugin add cascade-github`.
//
// SPORT: internal/plugins wiki-wiring (P1-E25-W5-S51-T6; live handle P1-PLG-03).

package plugins

import (
	"context"
)

// A TrustedHandle satisfies GitHubPluginCaller with no adapter code.
var _ GitHubPluginCaller = (*TrustedHandle)(nil)

// GitHubPluginCaller is the minimal transport cmd/cascade's wiki verbs
// need: one RPC method call against a live cascade-github process.
type GitHubPluginCaller interface {
	Call(ctx context.Context, method string, params []byte) ([]byte, error)
}

// NewGitHubWikiCaller returns a GitHubPluginCaller that launches
// cascade-github through provider on every call, dispatches through its live
// transport and closes it. A nil provider makes every call refuse.
func NewGitHubWikiCaller(provider HandleProvider) GitHubPluginCaller {
	return &githubWikiCaller{provider: provider}
}

type githubWikiCaller struct {
	provider HandleProvider
}

// Call implements GitHubPluginCaller.
func (c *githubWikiCaller) Call(ctx context.Context, method string, params []byte) ([]byte, error) {
	return callOnce(ctx, c.provider, "wiki", method, params)
}

// wikiHandleProvider is the HandleProvider NewGitHubWikiCallerForHost builds
// its caller over: production InstalledHandleProvider; a test may swap it
// (restoring it at cleanup, never in parallel with another swap).
var wikiHandleProvider = InstalledHandleProvider()

// NewGitHubWikiCallerForHost is the PRODUCTION entry point
// cmd/cascade/github_wiki_cmd.go builds `cascade github wiki sync`/`check`'s
// transport with. It returns the caller when cascade-github is installed as
// an enabled, trusted process plugin, and otherwise the typed refusal naming
// `cascade plugin add cascade-github`. The check launches nothing.
func NewGitHubWikiCallerForHost(ctx context.Context) (GitHubPluginCaller, error) {
	if err := installedPrereq(ctx, "`cascade github wiki sync`/`wiki check` need a live %s process to dispatch wiki.sync/wiki.check through"); err != nil {
		return nil, err
	}
	return NewGitHubWikiCaller(wikiHandleProvider), nil
}
