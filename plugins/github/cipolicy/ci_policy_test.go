// Purpose: ResolveCIPolicy/MatchesPrivate tests -- every
// routing branch the ticket's acceptance criterion names, plus the glob
// behaviour the earlier exact-match reading got wrong.
// SPORT: plugins.github.cipolicy.ResolveCIPolicy/TESTED (P1-E25-W5-S51-T5).
package cipolicy

import (
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestResolveCIPolicy_RoutingBranches(t *testing.T) {
	cfg := Config{PrivateRepoPatterns: []string{"acamarata/secret-repo"}, HasToken: true}

	cases := []struct {
		name      string
		ownerRepo string
		wantRoute Route
		wantErr   bool
	}{
		{"public repo with token passes through", "acamarata/cascade", RouteGitHubActions, false},
		{"private repo is refused to local", "acamarata/secret-repo", RouteLocal, true},
		{"private repo match is case-insensitive", "ACAMARATA/SECRET-REPO", RouteLocal, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			route, err := ResolveCIPolicy(cfg, c.ownerRepo)
			if route != c.wantRoute {
				t.Errorf("route = %q, want %q", route, c.wantRoute)
			}
			if (err != nil) != c.wantErr {
				t.Errorf("err = %v, wantErr = %v", err, c.wantErr)
			}
		})
	}
}

// TestResolveCIPolicy_OwnerWildcardRoutesLocal is the regression the
// exact-match implementation failed: "acamarata/*" must cover every
// repository under that owner. Under string equality it matched nothing,
// so a whole org routed to paid Actions.
func TestResolveCIPolicy_OwnerWildcardRoutesLocal(t *testing.T) {
	cfg := Config{PrivateRepoPatterns: []string{"acamarata/*"}, HasToken: true}
	route, err := ResolveCIPolicy(cfg, "acamarata/cascade")
	if route != RouteLocal {
		t.Errorf("route = %q, want %q for a repo under a wildcarded owner", route, RouteLocal)
	}
	if err == nil {
		t.Error("a wildcard-matched private repo must carry the never-pay refusal")
	}
	// The wildcard must not leak across the owner boundary: path.Match's
	// '*' does not cross '/', which is exactly the property that keeps
	// "acamarata/*" from claiming another owner's repositories.
	other, err := ResolveCIPolicy(cfg, "someone-else/cascade")
	if other != RouteGitHubActions || err != nil {
		t.Errorf("ResolveCIPolicy(other owner) = (%q, %v), want (github-actions, nil)", other, err)
	}
}

// TestResolveCIPolicy_RefusalIsActionable proves the private-repo refusal
// names `cascade ci run`, the acceptance criterion's exact wording.
func TestResolveCIPolicy_RefusalIsActionable(t *testing.T) {
	cfg := Config{PrivateRepoPatterns: []string{"acamarata/secret-repo"}, HasToken: true}
	_, err := ResolveCIPolicy(cfg, "acamarata/secret-repo")
	if err == nil {
		t.Fatal("expected a refusal error")
	}
	if !strings.Contains(err.Error(), "cascade ci run") {
		t.Errorf("error = %q, want it to mention `cascade ci run`", err.Error())
	}
}

// TestResolveCIPolicy_UnmatchedNoTokenRoutesLocal proves the default: an
// unmatched repo with no GitHub token configured routes local, with no
// refusal error (this is a default, not a policy violation).
func TestResolveCIPolicy_UnmatchedNoTokenRoutesLocal(t *testing.T) {
	route, err := ResolveCIPolicy(Config{HasToken: false}, "acamarata/some-public-repo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if route != RouteLocal {
		t.Errorf("route = %q, want %q", route, RouteLocal)
	}
}

// TestResolveCIPolicy_UnmatchedWithTokenRoutesActions proves the converse.
func TestResolveCIPolicy_UnmatchedWithTokenRoutesActions(t *testing.T) {
	route, err := ResolveCIPolicy(Config{HasToken: true}, "acamarata/some-public-repo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if route != RouteGitHubActions {
		t.Errorf("route = %q, want %q", route, RouteGitHubActions)
	}
}

// TestResolveCIPolicy_NoAllowPaidBypass is a structural proof of R-14.77:
// Config carries no field that could force a private repo onto Actions.
func TestResolveCIPolicy_NoAllowPaidBypass(t *testing.T) {
	cfg := Config{PrivateRepoPatterns: []string{"acamarata/secret-repo"}, HasToken: true}
	route, err := ResolveCIPolicy(cfg, "acamarata/secret-repo")
	if route == RouteGitHubActions || err == nil {
		t.Fatal("a private repo must never route to github-actions, regardless of HasToken")
	}
}

// TestResolveCIPolicy_RefusalIsPolicyDenied proves the refusal carries the
// taxonomy's KindPolicyDenied rather than a generic error.
func TestResolveCIPolicy_RefusalIsPolicyDenied(t *testing.T) {
	cfg := Config{PrivateRepoPatterns: []string{"acamarata/secret-repo"}}
	_, err := ResolveCIPolicy(cfg, "acamarata/secret-repo")
	kind, ok := cascade.KindOf(err)
	if !ok || kind != cascade.KindPolicyDenied {
		t.Errorf("KindOf(err) = (%v, %v), want (KindPolicyDenied, true)", kind, ok)
	}
}

// TestResolveCIPolicy_BadPatternRefuses proves an unparseable pattern is a
// refusal, never a silently skipped entry that would fail open.
func TestResolveCIPolicy_BadPatternRefuses(t *testing.T) {
	cfg := Config{PrivateRepoPatterns: []string{"acamarata/[bad"}, HasToken: true}
	route, err := ResolveCIPolicy(cfg, "acamarata/cascade")
	if err == nil {
		t.Fatal("expected a refusal for an unparseable pattern")
	}
	if route != RouteLocal {
		t.Errorf("route = %q, want the safe %q alongside the error", route, RouteLocal)
	}
}
