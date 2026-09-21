// Purpose: the CI provider policy resolver (P1-E25-W5-S51-T5 task 2) --
// the one place that decides where a repository's CI coverage belongs:
// GitHub Actions for a public repo with a token configured, the local
// `cascade ci run` gate for a repo the operator marked private. There is
// NO allow_paid/bypass parameter anywhere in this package -- R-14.77 is
// enforced by ResolveCIPolicy's signature, not by a flag a caller could
// set to defeat it.
//
// WHY THIS IS A SUBPACKAGE AND NOT plugins/github/ci_policy.go. The
// contract names plugins/github/ci_policy.go, and this file keeps that
// name and that exported function name. It sits one directory down
// because plugins/github itself is `package main` (main.go), and Go
// cannot import a main package -- so a resolver declared there can have
// no caller anywhere but its own test, which is the very gap CR-B
// rejected. The host composition bridge that actually consults this
// decision for the core `cascade ci` verbs lives in
// internal/plugins/ci_policy_wiring.go (Art.10.2 names internal/plugins
// as the one package allowed to import both internal/** and plugins/**),
// and it imports this package. Nothing here imports internal/**: this
// package sees pkg/cascade and the standard library only, exactly as
// Art.10.2 requires of every package under plugins/.
//
// Inputs: a Config (the private-repo patterns the operator declared under
// [ci.policy.repos.private], and whether a GitHub token is configured)
// plus an "owner/repo" string.
// Outputs: a Route, or a refusal error naming `cascade ci run` for a repo
// the policy keeps off paid CI.
// Constraints: no network call, no subprocess, no clock -- a pure local
// decision (06-FORGE-SPEC §5.15, no risk class).
// SPORT: plugins.github.cipolicy.ResolveCIPolicy/ADDED (P1-E25-W5-S51-T5).
package cipolicy

import (
	"path"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TokenVaultKey is the vault entry the cascade-github plugin stores its
// OAuth token under -- plugins/github/main.go's TokenKey() renders the
// same string, and that package's own ci_policy_test.go asserts the two
// agree, so the host-side token-presence probe and the plugin-side writer
// cannot drift apart without a test going red.
const TokenVaultKey = "cascade-github.oauth_token"

// Route names where a repository's CI coverage runs.
type Route string

// The two routes. There is deliberately no third: a repo either uses the
// free hosted lane or the local gate, and "paid" is not a state this type
// can represent.
const (
	RouteGitHubActions Route = "github-actions"
	RouteLocal         Route = "local"
)

// Config is the resolver's whole input surface.
type Config struct {
	// PrivateRepoPatterns are glob patterns over the "owner/repo" string,
	// matched with path.Match semantics case-insensitively, so
	// "acamarata/*" covers every repository under one owner. An exact
	// string is simply a pattern with no metacharacters.
	PrivateRepoPatterns []string
	// HasToken reports whether a GitHub token is configured. An unmatched
	// repo with no token routes local: without a token there is no way to
	// reach Actions at all, so local is the only truthful answer rather
	// than a policy refusal.
	HasToken bool
}

// ResolveCIPolicy is the never-pay routing decision. The returned Route is
// always meaningful, including alongside a non-nil error: a private repo
// resolves to RouteLocal AND carries the refusal a caller shows when it
// was asking about the Actions path.
func ResolveCIPolicy(cfg Config, ownerRepo string) (Route, error) {
	private, err := MatchesPrivate(cfg.PrivateRepoPatterns, ownerRepo)
	if err != nil {
		return RouteLocal, err
	}
	if private {
		return RouteLocal, cascade.New(cascade.KindPolicyDenied,
			"private repo: CI routed to local gate per never-pay policy -- run `cascade ci run`")
	}
	if !cfg.HasToken {
		return RouteLocal, nil
	}
	return RouteGitHubActions, nil
}

// MatchesPrivate reports whether ownerRepo matches any of patterns.
//
// Matching is case-insensitive because GitHub owner and repository names
// are, and it is glob rather than string equality because the config key
// is documented as a pattern list: the earlier exact-match reading made
// "acamarata/*" match nothing at all, which failed OPEN -- every repo
// under that owner routed to Actions. An unparseable pattern is refused
// rather than skipped, for the same reason: a pattern nobody can match is
// a pattern that silently stops protecting the repos it names.
func MatchesPrivate(patterns []string, ownerRepo string) (bool, error) {
	subject := strings.ToLower(strings.TrimSpace(ownerRepo))
	for _, p := range patterns {
		pattern := strings.ToLower(strings.TrimSpace(p))
		ok, err := path.Match(pattern, subject)
		if err != nil {
			return false, cascade.Wrapf(cascade.KindInvalidInput, err,
				"ci: %q is not a valid owner/repo pattern", p)
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}
