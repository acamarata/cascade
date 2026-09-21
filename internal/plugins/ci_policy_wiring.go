package plugins

import (
	"context"
	"strings"

	"github.com/acamarata/cascade/internal/ci"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/plugins/github/cipolicy"
)

// Purpose (this file): gives the CI provider policy resolver a real
//
//	caller. plugins/github/cipolicy decides where a repository's CI
//	belongs; internal/ci's `ci run` and Actions-polling verbs refuse or
//	proceed on that decision. Neither side can reach the other -- a plugin
//	may not import internal/**, and internal/ci may not import plugins/**
//	-- so the bridge lives here, the one package Art.10.2 allows to import
//	both, exactly as cascadepa_wiring.go bridges `cascade chat` to the
//	daemon transport.
//
// Inputs: none at import time. Each resolution reads [ci.policy] from the
//
//	operator's config.toml and probes the vault for the cascade-github
//	plugin's OAuth token, both resolved lazily so importing this package
//	never touches the environment (the same deferred-resolution discipline
//	cascadepa_wiring.go's pathResolver follows).
//
// Outputs: registers a ci.RouteResolver with internal/ci via
//
//	SetRouteResolver, so ProductionCmdDeps picks it up and the never-pay
//	policy is consulted by the verbs that could otherwise spend money
//	(R-14.77).
//
// Constraints: a token probe FAILURE is not a token. Every error path
//
//	reports "no token configured", which routes an unmatched repository to
//	the local gate -- the free side. Failing the other way would turn an
//	unreadable keychain into a reason to use paid CI.
//
// SPORT: internal/plugins:ci-policy-wiring (ADD) -- P1-E25-W5-S51-T5.

func init() {
	ci.SetRouteResolver(newCIRouteResolver(loadCIPolicyConfig, githubTokenConfigured))
}

// ciPolicyConfigLoader reads the operator's private-repo patterns.
type ciPolicyConfigLoader func(ctx context.Context) ([]string, error)

// tokenProbe reports whether a GitHub token is configured.
type tokenProbe func(ctx context.Context) bool

// newCIRouteResolver builds the resolver internal/ci consults. Both
// collaborators are injected so this file's own test proves every branch
// without a config file, a keychain, or a network call.
func newCIRouteResolver(load ciPolicyConfigLoader, hasToken tokenProbe) ci.RouteResolver {
	return ci.RouteResolverFunc(func(ctx context.Context, ownerRepo string) (ci.Route, error) {
		patterns, err := load(ctx)
		if err != nil {
			// A config that will not load is not a licence to route to
			// paid CI: report the safe route alongside the error and let
			// internal/ci's guards decide what to do with it.
			return ci.RouteLocal, err
		}
		route, resolveErr := cipolicy.ResolveCIPolicy(cipolicy.Config{
			PrivateRepoPatterns: patterns,
			HasToken:            hasToken(ctx),
		}, ownerRepo)
		return coreRoute(route), resolveErr
	})
}

// coreRoute maps the plugin-side Route onto internal/ci's own. The two
// types carry identical strings on purpose, but they are distinct types
// across the boundary, and this is the one place that crosses it.
func coreRoute(r cipolicy.Route) ci.Route {
	if r == cipolicy.RouteGitHubActions {
		return ci.RouteGitHubActions
	}
	return ci.RouteLocal
}

// loadCIPolicyConfig reads [ci.policy.repos.private] from the operator's
// config.toml. Resolution is per-call rather than cached: `cascade ci run`
// is a one-shot command, and a long-lived daemon that later consults this
// should see an edited config without a restart.
func loadCIPolicyConfig(ctx context.Context) ([]string, error) {
	paths, err := runtime.NewDefaultPathProvider()
	if err != nil {
		return nil, err
	}
	cfg, err := runtime.Load(ctx, runtime.LoadOptions{Path: paths.ConfigPath()})
	if err != nil {
		return nil, err
	}
	return cfg.CIPolicy.PrivateRepos, nil
}

// githubTokenConfigured reports whether the cascade-github plugin's OAuth
// token is in the vault.
//
// It reads the vault's NAME INDEX (Custody.List), never a secret value:
// presence is the whole question, and on darwin List resolves one index
// entry rather than walking the keychain, which is what keeps this probe
// from turning a non-interactive command into a prompt (06 §5.8). Any
// failure -- no custody backend, an unreadable store -- reports false, for
// the reason this file's header states.
func githubTokenConfigured(ctx context.Context) bool {
	paths, err := runtime.NewDefaultPathProvider()
	if err != nil {
		return false
	}
	custody, err := secrets.SelectCustody(secrets.Config{
		Service: secrets.DefaultVaultService, Dir: paths.DataDir(),
	})
	if err != nil {
		return false
	}
	names, err := custody.List(ctx)
	if err != nil {
		return false
	}
	return containsName(names, cipolicy.TokenVaultKey)
}

// containsName reports whether names holds want, compared the way vault
// entry names are: exactly, after trimming.
func containsName(names []string, want string) bool {
	for _, n := range names {
		if strings.TrimSpace(n) == want {
			return true
		}
	}
	return false
}
