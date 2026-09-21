// Purpose: newCIRouteResolver's tests -- the bridge that gives the CI
// provider policy resolver its production caller. Both collaborators are
// injected, so no test here reads a config file, touches a keychain, or
// makes a network call.
// SPORT: internal/plugins:ci-policy-wiring (TESTED) -- P1-E25-W5-S51-T5.
package plugins

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/ci"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/plugins/github/cipolicy"
)

func patterns(p ...string) ciPolicyConfigLoader {
	return func(context.Context) ([]string, error) { return p, nil }
}

func tokenPresent(present bool) tokenProbe {
	return func(context.Context) bool { return present }
}

// TestCIRouteResolver_PrivatePatternRoutesLocal proves the bridge carries
// both halves of the policy's answer across the boundary: the local route
// AND the refusal that goes with it, which is what lets internal/ci's
// guards tell "run locally" from "do not poll Actions".
func TestCIRouteResolver_PrivatePatternRoutesLocal(t *testing.T) {
	r := newCIRouteResolver(patterns("acamarata/*"), tokenPresent(true))
	route, err := r.Route(context.Background(), "acamarata/cascade")
	if route != ci.RouteLocal {
		t.Errorf("route = %q, want %q", route, ci.RouteLocal)
	}
	if err == nil {
		t.Fatal("a private repo must carry the never-pay refusal")
	}
	kind, ok := cascade.KindOf(err)
	if !ok || kind != cascade.KindPolicyDenied {
		t.Errorf("KindOf(err) = (%v, %v), want (KindPolicyDenied, true)", kind, ok)
	}
}

func TestCIRouteResolver_PublicWithTokenRoutesActions(t *testing.T) {
	r := newCIRouteResolver(patterns("acamarata/secret"), tokenPresent(true))
	route, err := r.Route(context.Background(), "acamarata/cascade")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if route != ci.RouteGitHubActions {
		t.Errorf("route = %q, want %q", route, ci.RouteGitHubActions)
	}
}

// TestCIRouteResolver_NoTokenRoutesLocal proves the token probe is actually
// consulted: the same config with no token routes local instead.
func TestCIRouteResolver_NoTokenRoutesLocal(t *testing.T) {
	r := newCIRouteResolver(patterns(), tokenPresent(false))
	route, err := r.Route(context.Background(), "acamarata/cascade")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if route != ci.RouteLocal {
		t.Errorf("route = %q, want %q with no token configured", route, ci.RouteLocal)
	}
}

// TestCIRouteResolver_ConfigFailureReportsTheSafeRoute proves an unreadable
// config is not a licence to spend: the error surfaces, and the route
// reported alongside it is the free one.
func TestCIRouteResolver_ConfigFailureReportsTheSafeRoute(t *testing.T) {
	boom := cascade.New(cascade.KindUnavailable, "config unreadable")
	load := func(context.Context) ([]string, error) { return nil, boom }
	route, err := newCIRouteResolver(load, tokenPresent(true)).Route(context.Background(), "acamarata/cascade")
	if err == nil {
		t.Fatal("expected the config failure to surface")
	}
	if route != ci.RouteLocal {
		t.Errorf("route = %q, want the safe %q alongside the error", route, ci.RouteLocal)
	}
}

func TestCoreRoute(t *testing.T) {
	if got := coreRoute(cipolicy.RouteGitHubActions); got != ci.RouteGitHubActions {
		t.Errorf("coreRoute(github-actions) = %q, want %q", got, ci.RouteGitHubActions)
	}
	if got := coreRoute(cipolicy.RouteLocal); got != ci.RouteLocal {
		t.Errorf("coreRoute(local) = %q, want %q", got, ci.RouteLocal)
	}
	// An unrecognised value maps to the free side, never to paid CI.
	if got := coreRoute(cipolicy.Route("something-else")); got != ci.RouteLocal {
		t.Errorf("coreRoute(unknown) = %q, want %q", got, ci.RouteLocal)
	}
}

func TestContainsName(t *testing.T) {
	names := []string{"other.key", "  " + cipolicy.TokenVaultKey + "  "}
	if !containsName(names, cipolicy.TokenVaultKey) {
		t.Error("containsName missed the token key; vault names are compared after trimming")
	}
	if containsName(names, "absent.key") {
		t.Error("containsName matched a name the vault does not hold")
	}
}

// TestInitInstalledTheResolver proves this file's init() actually wired the
// bridge -- the whole point of the ticket's "the resolver gets a caller"
// fix. Without it, internal/ci's ProductionCmdDeps would read nil and every
// `ci run` would fail closed.
func TestInitInstalledTheResolver(t *testing.T) {
	if ci.InstalledRouteResolver() == nil {
		t.Fatal("importing internal/plugins must install a ci.RouteResolver; nothing did")
	}
}
