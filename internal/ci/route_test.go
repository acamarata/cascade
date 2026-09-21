// Purpose: the never-pay guards' tests -- the four decisions D3 names
// (private pattern: run proceeds, Actions refused; public with a token:
// run refused naming Actions, polling proceeds), the fail-closed
// no-resolver case, and parseOwnerRepo's remote-URL shapes.
//
// allowActionsRoutes and localOnlyRoutes below are also the resolvers the
// T2 polling tests in poll_test.go/poll_pagination_test.go pass: those
// exercise the Actions transport, which now refuses without a policy.
// SPORT: internal.ci.RouteResolver/TESTED (P1-E25-W5-S51-T5).
package ci

import (
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// allowActionsRoutes routes every repository to hosted Actions.
var allowActionsRoutes = RouteResolverFunc(func(_ context.Context, _ string) (Route, error) {
	return RouteGitHubActions, nil
})

// localOnlyRoutes routes every repository to the local gate, carrying the
// same KindPolicyDenied refusal the real resolver attaches for a private
// repo -- so a guard that mishandled the "route plus refusal" pair would
// be caught here rather than only in production.
var localOnlyRoutes = RouteResolverFunc(func(_ context.Context, _ string) (Route, error) {
	return RouteLocal, cascade.New(cascade.KindPolicyDenied,
		"private repo: CI routed to local gate per never-pay policy -- run `cascade ci run`")
})

// unavailableRoutes fails to reach a verdict at all (a config that will not
// load), as distinct from reaching the verdict "local".
var unavailableRoutes = RouteResolverFunc(func(_ context.Context, _ string) (Route, error) {
	return RouteLocal, cascade.New(cascade.KindUnavailable, "ci: config unreadable")
})

func TestGuardLocalRun_PrivateRepoProceeds(t *testing.T) {
	if err := guardLocalRun(context.Background(), localOnlyRoutes, "acamarata/secret"); err != nil {
		t.Fatalf("a repo the policy routes local must be allowed to run the local gate: %v", err)
	}
}

func TestGuardLocalRun_ActionsRepoRefused(t *testing.T) {
	err := guardLocalRun(context.Background(), allowActionsRoutes, "acamarata/cascade")
	if err == nil {
		t.Fatal("expected a refusal for a repo whose CI runs on hosted Actions")
	}
	if !strings.Contains(err.Error(), string(RouteGitHubActions)) {
		t.Errorf("error = %q, want it to name the %q route", err.Error(), RouteGitHubActions)
	}
	assertKind(t, err, cascade.KindPolicyDenied)
}

func TestGuardLocalRun_NoResolverFailsClosed(t *testing.T) {
	err := guardLocalRun(context.Background(), nil, "acamarata/cascade")
	if err == nil {
		t.Fatal("a missing policy resolver must refuse, never be read as \"assume local\"")
	}
	assertKind(t, err, cascade.KindUnavailable)
}

// TestGuardLocalRun_UnnamedRepoProceeds proves a checkout with no GitHub
// remote can still run the local gate -- there is no hosted Actions for it
// to be routed to. The nil-resolver refusal above still applies first.
func TestGuardLocalRun_UnnamedRepoProceeds(t *testing.T) {
	if err := guardLocalRun(context.Background(), allowActionsRoutes, "  "); err != nil {
		t.Fatalf("a checkout with no origin remote must still run locally: %v", err)
	}
}

// TestGuardLocalRun_ConfigFailureRefuses proves an unreachable policy is
// not the same as the verdict "local": it refuses, carrying its own kind.
func TestGuardLocalRun_ConfigFailureRefuses(t *testing.T) {
	err := guardLocalRun(context.Background(), unavailableRoutes, "acamarata/cascade")
	if err == nil {
		t.Fatal("a policy that could not be read must refuse")
	}
	assertKind(t, err, cascade.KindUnavailable)
}

func TestGuardActionsPoll_LocalRepoRefused(t *testing.T) {
	err := guardActionsPoll(context.Background(), localOnlyRoutes, "acamarata/secret")
	if err == nil {
		t.Fatal("expected a refusal to poll Actions for a locally-routed repo")
	}
	if !strings.Contains(err.Error(), "cascade ci run") {
		t.Errorf("error = %q, want it to point at `cascade ci run`", err.Error())
	}
	assertKind(t, err, cascade.KindPolicyDenied)
}

func TestGuardActionsPoll_ActionsRepoProceeds(t *testing.T) {
	if err := guardActionsPoll(context.Background(), allowActionsRoutes, "acamarata/cascade"); err != nil {
		t.Fatalf("a repo routed to hosted Actions must be pollable: %v", err)
	}
}

func TestGuardActionsPoll_NoResolverFailsClosed(t *testing.T) {
	err := guardActionsPoll(context.Background(), nil, "acamarata/cascade")
	if err == nil {
		t.Fatal("a missing policy resolver must refuse the paid direction")
	}
	assertKind(t, err, cascade.KindUnavailable)
}

// TestGuardActionsPoll_UnnamedRepoRefused proves the paid direction fails
// closed where the local one fails open: with no "owner/repo" there is no
// way to know the policy allows this spend.
func TestGuardActionsPoll_UnnamedRepoRefused(t *testing.T) {
	err := guardActionsPoll(context.Background(), allowActionsRoutes, "")
	if err == nil {
		t.Fatal("expected a refusal to poll Actions for an unnamed repository")
	}
	assertKind(t, err, cascade.KindPolicyDenied)
}

func TestGuardActionsPoll_ConfigFailureRefuses(t *testing.T) {
	// unavailableRoutes answers RouteLocal, so the local-route refusal is
	// what fires; either way the call does not proceed, which is the
	// property that matters for a paid request.
	if err := guardActionsPoll(context.Background(), unavailableRoutes, "acamarata/cascade"); err == nil {
		t.Fatal("a policy that could not be read must refuse the paid direction")
	}
}

func TestRouteResolverRegistry(t *testing.T) {
	t.Cleanup(func() { SetRouteResolver(nil) })
	if InstalledRouteResolver() != nil {
		t.Skip("another test in this package left a resolver installed")
	}
	SetRouteResolver(allowActionsRoutes)
	if InstalledRouteResolver() == nil {
		t.Fatal("SetRouteResolver did not install the resolver")
	}
	SetRouteResolver(nil)
	if InstalledRouteResolver() != nil {
		t.Error("SetRouteResolver(nil) must clear the registry, leaving verbs failing closed")
	}
}

func TestParseOwnerRepo(t *testing.T) {
	cases := []struct {
		remote string
		want   string
		ok     bool
	}{
		{"https://github.com/acamarata/cascade.git\n", "acamarata/cascade", true},
		{"https://github.com/acamarata/cascade", "acamarata/cascade", true},
		{"git@github.com:acamarata/cascade.git", "acamarata/cascade", true},
		{"ssh://git@github.com/acamarata/cascade.git", "acamarata/cascade", true},
		{"https://example.com/team/group/repo.git", "group/repo", true},
		{"", "", false},
		{"not-a-url", "", false},
	}
	for _, c := range cases {
		t.Run(c.remote, func(t *testing.T) {
			got, ok := parseOwnerRepo(c.remote)
			if ok != c.ok || got != c.want {
				t.Errorf("parseOwnerRepo(%q) = (%q, %v), want (%q, %v)", c.remote, got, ok, c.want, c.ok)
			}
		})
	}
}

// assertKind checks err carries kind. (*cascade.Error).Is compares Kind
// only, so a kind assertion is the meaningful classification check here --
// an identity comparison against a sentinel would pass for any error of
// the same kind (lesson_errors_is_compares_kind_only).
func assertKind(t *testing.T, err error, want cascade.Kind) {
	t.Helper()
	kind, ok := cascade.KindOf(err)
	if !ok || kind != want {
		t.Errorf("KindOf(%v) = (%v, %v), want (%v, true)", err, kind, ok, want)
	}
}
