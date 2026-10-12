// Purpose: the PRODUCTION composition for wait-on-green/merge-on-green
// (P1-E25-W5-S51-T3). WaitOnGreen and MergeOnGreen take their
// collaborators by injection; something has to build the real ones, and
// core is where the GitHub-polling half of that belongs (cmd/cascade owns
// the CLI half -- see cmd/cascade/github_ci_cmd.go).
//
// Inputs: the process environment (for the API token), an injected
// runtime.Clock, the composition root's Doer constructor and its engine
// constructor (production: egress.ProcessEngine, the process firewall).
//
// Outputs: a WaitDeps over the real S-51.T2 Client -- the caller's Doer,
// the real ci-poll egress engine, the installed never-pay route resolver
// and the real ContextSleep -- or a TYPED refusal naming the prerequisite
// that is missing. Plus ClientHeadSHA, the production HeadSHAFetcher
// merge-on-green re-checks its target with.
//
// WHY THE DOER IS INJECTED RATHER THAN BUILT HERE. poll.go's header states
// the split this package keeps to: "a Doer (production: a *http.Client
// adapter built by the composition root ... this package never imports
// net/http)". An adapter built in this file would break exactly that, and
// with it internal/build's egress import allowlist, which does not name
// internal/ci. So newDoer arrives from cmd/cascade (github_ci_doer.go),
// the same shape internal/providers/intake uses.
//
// Constraints: NO fail-open default anywhere in this file. A missing token
// is a refusal, not an anonymous request; a detector or engine that will
// not build is a refusal, not a nil engine. The engine is the process
// firewall's, bound to the real vault: this package never builds an engine
// and never supplies a value source of its own, so the day this path sends a
// body, the exact-value pass already covers it.
//
// SPORT: internal.ci.WaitDepsFromEnv/ADDED, internal.ci.ClientHeadSHA/ADDED
//
//	(P1-E25-W5-S51-T3).

package ci

import (
	"context"

	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// waitTokenEnvNames are the environment variables wait-on-green reads its
// GitHub API token from, in order of precedence.
var waitTokenEnvNames = []string{"CASCADE_GITHUB_TOKEN", "GITHUB_TOKEN", "GH_TOKEN"}

// ErrNoGitHubToken is the refusal WaitDepsFromEnv returns when no token is
// available. It names both the variables an operator can set and the
// reason the plugin's own vault-stored token is not reachable from here.
func ErrNoGitHubToken() error {
	return cascade.New(cascade.KindInvalidInput,
		"ci: wait-on-green needs a GitHub API token: set CASCADE_GITHUB_TOKEN, GITHUB_TOKEN or GH_TOKEN. "+
			"The cascade-github plugin's vault-stored token is held by that plugin's own process, which this "+
			"host cannot launch yet (see internal/plugins/dispatch.go's ProvisionElevated), so it is not an "+
			"alternative today")
}

// NewTokenDoer builds the outbound transport for one resolved GitHub API
// token. The composition root supplies it (see this file's header).
type NewTokenDoer func(token string) Doer

// WaitDepsFromEnv builds the production WaitDeps over the Doer newDoer
// builds for the token it resolves and the engine newEngine returns for the
// process's default registry. A nil newEngine is KindUnavailable: there is
// no outbound path without a firewall.
func WaitDepsFromEnv(newDoer NewTokenDoer, getenv runtime.Getenv, clock runtime.Clock,
	newEngine func(*egress.Registry) (*egress.Engine, error),
) (WaitDeps, error) {
	if newDoer == nil || getenv == nil || clock == nil {
		return WaitDeps{}, cascade.New(cascade.KindInvalidInput,
			"ci: WaitDepsFromEnv requires a Doer constructor, a getenv and a clock")
	}
	if newEngine == nil {
		return WaitDeps{}, cascade.New(cascade.KindUnavailable,
			"ci: WaitDepsFromEnv requires the process egress firewall")
	}
	token := ""
	for _, name := range waitTokenEnvNames {
		if v := getenv(name); v != "" {
			token = v
			break
		}
	}
	if token == "" {
		return WaitDeps{}, ErrNoGitHubToken()
	}
	// The engine error is returned unchanged: a custody failure keeps its
	// Kind and cause.
	engine, err := newEngine(egress.DefaultRegistry())
	if err != nil {
		return WaitDeps{}, err
	}
	doer := newDoer(token)
	if doer == nil {
		return WaitDeps{}, cascade.New(cascade.KindInvalidInput,
			"ci: the injected Doer constructor returned no Doer")
	}
	// repoID 0: WaitOnGreen never writes a ci_run row (it reads the live
	// API and returns a decision), and the id only labels persisted rows.
	client := NewClient(doer, engine, "", 0, InstalledRouteResolver())
	return WaitDeps{Client: client, Clock: clock, Sleep: ContextSleep}, nil
}

// ClientHeadSHA is the production HeadSHAFetcher: it re-reads the run list
// through the SAME never-pay-guarded client the wait polled with, and
// reports the head SHA the newest run for ref now carries. A ref with no
// run at all yields an empty SHA and no error, which rebindCheck's caller
// (recheckHead) treats as a mismatch -- refusing, never merging.
func ClientHeadSHA(client *Client) HeadSHAFetcher {
	return func(ctx context.Context, owner, repo, ref string) (string, error) {
		if client == nil {
			return "", cascade.New(cascade.KindInvalidInput, "ci: the head-SHA re-check has no Client")
		}
		result, err := client.PollRuns(ctx, owner, repo)
		if err != nil {
			return "", err
		}
		if result.NotModified {
			// "Nothing changed since our last conditional request" is not
			// evidence about the head: a caller that cannot re-read must
			// refuse rather than assume the ref stood still.
			return "", cascade.New(cascade.KindUnavailable,
				"ci: GitHub answered 304 Not Modified, so the current head SHA could not be re-read")
		}
		run, ok := findRunForRef(result.Runs, ref)
		if !ok {
			return "", nil
		}
		return run.HeadSHA, nil
	}
}
