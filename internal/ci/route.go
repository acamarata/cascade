// Purpose: the never-pay routing seam both real `ci` verbs consult
// (P1-E25-W5-S51-T5, R-14.77). The DECISION lives in
// plugins/github/cipolicy; this file is the core-side port it arrives
// through, plus the two guards that turn a Route into an action:
//
//   - guardLocalRun: `cascade ci run` refuses to burn a local gate for a
//     repository whose CI the operator put on hosted Actions.
//   - guardActionsPoll: the Actions polling path (poll.go) refuses to
//     spend a request on a repository the policy keeps local -- that is
//     the direction R-14.77 exists to stop, so it fails CLOSED.
//
// Inputs: a RouteResolver (injected -- internal/plugins/ci_policy_wiring.go
// installs the production one at init, a test passes its own) and an
// "owner/repo" string.
// Outputs: nil when the verb may proceed, a typed refusal otherwise.
// Constraints: NO resolver means refuse (KindUnavailable), never "assume
// local". A missing policy is missing infrastructure, and the one thing a
// never-pay rule must not do is guess.
// SPORT: internal.ci.RouteResolver/ADDED, internal.ci.SetRouteResolver/ADDED
//
//	(P1-E25-W5-S51-T5).

package ci

import (
	"context"
	"strings"
	"sync"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Route names where a repository's CI coverage runs. Declared here rather
// than imported from plugins/github/cipolicy because internal/ci is an
// ordinary core package and may not import plugins/** at all (Art.10.2);
// internal/plugins, the one package allowed to import both, maps between
// the two values.
type Route string

// The two routes, with the same wire strings the ci_run_source table
// records so a refusal message and a stored row read alike.
const (
	RouteGitHubActions Route = SourceGitHubActions
	RouteLocal         Route = SourceLocal
)

// RouteResolver reports where one repository's CI belongs.
type RouteResolver interface {
	// Route resolves ownerRepo. A returned error is the policy's own
	// refusal or a config failure; the Route is still meaningful for the
	// refusal case (see cipolicy.ResolveCIPolicy).
	Route(ctx context.Context, ownerRepo string) (Route, error)
}

// RouteResolverFunc adapts a plain function to RouteResolver, so the
// composition bridge can install a closure over config + token presence
// without declaring a type for it.
type RouteResolverFunc func(ctx context.Context, ownerRepo string) (Route, error)

// Route implements RouteResolver.
func (f RouteResolverFunc) Route(ctx context.Context, ownerRepo string) (Route, error) {
	return f(ctx, ownerRepo)
}

// installed holds the process-wide resolver the production composition
// bridge registers. A package-level registry (rather than a parameter
// threaded from main) is what lets internal/plugins inject it from an
// init(), matching the SetClient bridge cascadepa_wiring.go already uses
// for `cascade chat`; ProductionCmdDeps reads it, and every test passes
// its own through CmdDeps instead of touching this.
var installed struct {
	mu       sync.RWMutex
	resolver RouteResolver
}

// SetRouteResolver installs the process-wide never-pay resolver. Calling
// it twice replaces the previous one; passing nil clears it, which leaves
// every guarded verb failing closed rather than unguarded.
func SetRouteResolver(r RouteResolver) {
	installed.mu.Lock()
	defer installed.mu.Unlock()
	installed.resolver = r
}

// InstalledRouteResolver returns the registered resolver, or nil when no
// composition bridge has installed one.
func InstalledRouteResolver() RouteResolver {
	installed.mu.RLock()
	defer installed.mu.RUnlock()
	return installed.resolver
}

// errNoPolicy is both guards' fail-closed outcome. KindUnavailable, not
// KindPolicyDenied: nothing was denied by a policy, the policy itself
// could not be consulted.
func errNoPolicy() error {
	return cascade.New(cascade.KindUnavailable,
		"ci: no never-pay policy resolver is configured; cannot decide whether this repository's CI belongs on GitHub Actions or the local gate")
}

// guardLocalRun decides whether `cascade ci run` may run the local gate
// for ownerRepo.
//
// An EMPTY ownerRepo proceeds: a working tree with no GitHub remote has no
// hosted Actions to be routed to, and refusing there would make the local
// gate unusable outside GitHub entirely. The nil-resolver check comes
// first regardless, so "no policy installed" is still a refusal and not a
// side effect of an unnamed repository.
func guardLocalRun(ctx context.Context, r RouteResolver, ownerRepo string) error {
	if r == nil {
		return errNoPolicy()
	}
	if strings.TrimSpace(ownerRepo) == "" {
		return nil
	}
	route, err := r.Route(ctx, ownerRepo)
	if err != nil && !isPolicyRefusal(err) {
		// A config or infrastructure failure, not a routing verdict:
		// refuse rather than run a gate whose policy could not be read.
		return err
	}
	if route == RouteGitHubActions {
		return cascade.Newf(cascade.KindPolicyDenied,
			"ci: %s routes to %s per [ci.policy]; its CI runs on hosted GitHub Actions, not the local gate", ownerRepo, RouteGitHubActions)
	}
	// A KindPolicyDenied refusal here says "this repository belongs on the
	// local gate", which is precisely what `ci run` was asked to do -- the
	// refusal is for the Actions path, so running locally is the outcome
	// the policy wants, not something to report as an error.
	return nil
}

// isPolicyRefusal reports whether err is the policy's own routing verdict
// (KindPolicyDenied) rather than a failure to reach a verdict at all.
func isPolicyRefusal(err error) bool {
	kind, ok := cascade.KindOf(err)
	return ok && kind == cascade.KindPolicyDenied
}

// guardActionsPoll decides whether the Actions polling path may spend a
// request on ownerRepo. This is the paid direction, so it refuses on
// anything short of an explicit github-actions route: no resolver, a
// config failure, an unnamed repository, and a local route all stop the
// call before a byte leaves (R-14.77).
func guardActionsPoll(ctx context.Context, r RouteResolver, ownerRepo string) error {
	if r == nil {
		return errNoPolicy()
	}
	if strings.TrimSpace(ownerRepo) == "" {
		return cascade.New(cascade.KindPolicyDenied,
			"ci: refusing to poll GitHub Actions for an unnamed repository; [ci.policy] routes by \"owner/repo\"")
	}
	route, err := r.Route(ctx, ownerRepo)
	if route == RouteLocal {
		return cascade.Newf(cascade.KindPolicyDenied,
			"ci: %s routes to the %s gate per [ci.policy]; refusing to poll GitHub Actions for it -- run `cascade ci run`", ownerRepo, RouteLocal)
	}
	if err != nil {
		return err
	}
	return nil
}

// parseOwnerRepo extracts "owner/repo" from a git remote URL, in either of
// the two shapes git writes: an https URL
// ("https://github.com/owner/repo.git") or scp-style ssh
// ("git@github.com:owner/repo.git"). Anything it cannot read with
// confidence yields "", false -- guardLocalRun treats an unnamed
// repository as local-gate-eligible, and guessing an owner would be worse
// than admitting the remote was not understood.
func parseOwnerRepo(remote string) (string, bool) {
	s := strings.TrimSpace(remote)
	s = strings.TrimSuffix(s, "/")
	s = strings.TrimSuffix(s, ".git")
	if s == "" {
		return "", false
	}
	if at := strings.LastIndex(s, "@"); at >= 0 {
		if colon := strings.Index(s[at:], ":"); colon >= 0 {
			s = s[at+colon+1:]
		}
	}
	if scheme := strings.Index(s, "://"); scheme >= 0 {
		rest := s[scheme+3:]
		slash := strings.Index(rest, "/")
		if slash < 0 {
			return "", false
		}
		s = rest[slash+1:]
	}
	parts := strings.Split(strings.Trim(s, "/"), "/")
	if len(parts) < 2 {
		return "", false
	}
	owner, repo := parts[len(parts)-2], parts[len(parts)-1]
	if owner == "" || repo == "" {
		return "", false
	}
	return owner + "/" + repo, true
}
