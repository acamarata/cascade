// Purpose: the PRODUCTION composition for wait-on-green/merge-on-green
// (P1-E25-W5-S51-T3). WaitOnGreen and MergeOnGreen take their
// collaborators by injection; something has to build the real ones, and
// core is where the GitHub-polling half of that belongs (cmd/cascade owns
// the CLI half -- see cmd/cascade/github_ci_cmd.go).
//
// Inputs: the process environment (for the API token), an injected
// runtime.Clock, and the composition root's Doer constructor.
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
// not build is a refusal, not a nil engine. The egress vault is
// deliberately EMPTY and that is not a stand-in: the ci-poll class sends
// no request body (poll.go issues GETs and consults the engine for the
// class capability and the sensitivity pass only, never Intercept), so
// there is no outbound content for a substitution pass to redact. The day
// this path sends a body, it needs the real vault, and this comment is
// where that is recorded.
//
// SPORT: internal.ci.WaitDepsFromEnv/ADDED, internal.ci.ClientHeadSHA/ADDED
//
//	(P1-E25-W5-S51-T3).

package ci

import (
	"context"

	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
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
// builds for the token it resolves.
func WaitDepsFromEnv(newDoer NewTokenDoer, getenv runtime.Getenv, clock runtime.Clock) (WaitDeps, error) {
	if newDoer == nil || getenv == nil || clock == nil {
		return WaitDeps{}, cascade.New(cascade.KindInvalidInput,
			"ci: WaitDepsFromEnv requires a Doer constructor, a getenv and a clock")
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
	engine, err := newCIPollEgressEngine()
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

// newCIPollEgressEngine builds the real egress engine the ci-poll class is
// registered in (see this file's header for the empty-vault reasoning).
func newCIPollEgressEngine() (*egress.Engine, error) {
	detector, err := secrets.NewDetector(secrets.DefaultRegistry(), secrets.DefaultDetectionConfig())
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "ci: building the outbound secrets detector")
	}
	engine, err := egress.NewEngine(egress.DefaultRegistry(), emptyEgressVault{}, detector)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "ci: building the ci-poll egress engine")
	}
	return engine, nil
}

// emptyEgressVault is an egress.Vault holding nothing. See the header.
type emptyEgressVault struct{}

// List implements egress.Vault.
func (emptyEgressVault) List(context.Context) ([]string, error) { return nil, nil }

// Get implements egress.Vault: nothing is stored, so nothing is found.
func (emptyEgressVault) Get(_ context.Context, name string) ([]byte, error) {
	return nil, cascade.Newf(cascade.KindNotFound, "ci: no egress vault entry named %q on the ci-poll path", name)
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
