package cascadepa

// Purpose (this file): coverage for plugin.go's Provides.Intents
//   declaration and the ACCEPTANCE proof (P1-E24-W5-S50-T4, round-1
//   adversarial CR fix item 6) that the plugin's own intent route --
//   handlers{}.DispatchIntent (commands.go), the plugin.BuiltinHandlers
//   method the compile-time registry calls -- genuinely reaches
//   plugins/cascade-pa/install.DispatchIntent and a real Flow.Run, not
//   just that install.DispatchIntent works in isolation (already proven by
//   that package's own tests).
//
// DISCLOSED GAP (not papered over): no production caller of
//   BuiltinHandlers.DispatchIntent exists anywhere in this tree today
//   (grepped: only per-plugin implementations and tests reference the
//   method). Building a host-level intent router that resolves an intent
//   string to the plugin declaring it and calls this method is out of
//   this ticket's files_scope (plugin.go's own hunk is deliberately
//   minimal) and no existing ticket in the planning corpus owns it. This
//   test proves the PLUGIN's half of the contract -- manifest declaration
//   plus a working route from the BuiltinHandlers interface down to a
//   real install.Flow -- which is what fix item 6 asked for; the
//   host-level router remains a real, named, disclosed gap.
// SPORT: plugins/cascade-pa:cmd:chat (CHANGED) -- P1-E24-W5-S50-T4.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
	"github.com/acamarata/cascade/plugins/cascade-pa/install"
)

// TestManifestDeclaresInstallIntent proves manifest()'s Provides.Intents
// names install.IntentName -- without this a caller has no way to learn
// cascade-pa can satisfy it at all.
func TestManifestDeclaresInstallIntent(t *testing.T) {
	m := manifest()
	found := false
	for _, is := range m.Provides.Intents {
		if is.Name == install.IntentName {
			found = true
			if is.Description == "" {
				t.Error("IntentSpec.Description is empty")
			}
		}
	}
	if !found {
		t.Fatalf("Provides.Intents = %+v, want an entry named %q", m.Provides.Intents, install.IntentName)
	}
}

// fakeIntentResolver, fakeIntentConfirm, fakeIntentInstaller,
// fakeIntentElevator and fakeIntentBus are the five install.Deps
// collaborators, faked here (this package cannot import package install's
// own unexported flow_test.go fakes -- different package) so this test
// drives a REAL install.Flow through handlers{}.DispatchIntent end to end.
type fakeIntentResolver struct{ cand plugin.Candidate }

func (f fakeIntentResolver) Resolve(context.Context, plugin.ManifestSet, *plugin.VerifiedIndex, string) ([]plugin.Candidate, error) {
	return []plugin.Candidate{f.cand}, nil
}

type fakeIntentConfirm struct{}

func (fakeIntentConfirm) Confirm(context.Context, install.Proposal) (install.ConfirmOutcome, error) {
	return install.ConfirmYes, nil
}

type fakeIntentInstaller struct{}

func (fakeIntentInstaller) Add(context.Context, install.AddRequest) (install.AddResult, error) {
	return install.AddResult{Outcome: install.AddOutcomeInstalled}, nil
}

type fakeIntentElevator struct{}

func (fakeIntentElevator) Elevate(context.Context, install.ElevationRequest) (install.ElevationResult, error) {
	return install.ElevationResult{}, cascade.New(cascade.KindUnavailable, "unused in this acceptance test")
}

type fakeIntentBus struct{}

func (fakeIntentBus) Publish(context.Context, install.Event) error { return nil }

// TestHandlersDispatchIntent_RoutesInstallFlow is the acceptance test fix
// item 6 asked for: handlers{}.DispatchIntent(ctx, install.IntentName,
// ...) -- the SAME method plugin.BuiltinHandlers declares and a host would
// call -- reaches a real install.Flow and returns its real result, proving
// the plugin's own intent route (not just install.DispatchIntent alone).
func TestHandlersDispatchIntent_RoutesInstallFlow(t *testing.T) {
	cand := plugin.Candidate{
		PluginID: "git-tools", Name: "Git Tools", Source: plugin.CandidateSourceInstalled,
		Manifest: plugin.Manifest{ID: "git-tools", Name: "Git Tools", Version: "1.0.0", Runtime: plugin.RuntimeBuiltin},
	}
	f := install.NewFlow(install.Deps{
		Resolver: fakeIntentResolver{cand: cand},
		Confirm:  fakeIntentConfirm{},
		Install:  fakeIntentInstaller{},
		Elevate:  fakeIntentElevator{},
		Events:   fakeIntentBus{},
		Getenv:   func(string) string { return "" },
	})
	install.SetFlow(f)
	t.Cleanup(func() { install.SetFlow(nil) })

	input, err := json.Marshal(struct {
		Intent string `json:"intent"`
	}{Intent: "git push"})
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	out, err := (handlers{}).DispatchIntent(context.Background(), install.IntentName, input)
	if err != nil {
		t.Fatalf("DispatchIntent: %v", err)
	}
	var result install.RunResult
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if !result.Resumed {
		t.Fatalf("RunResult = %+v, want Resumed=true -- the route did not reach a real, completed install", result)
	}
}

// TestHandlersDispatchIntent_UnwiredFlowRefuses proves the route reaches
// install.DispatchIntent's own fail-closed default when no Flow has been
// set -- the acceptance path never fabricates a success.
func TestHandlersDispatchIntent_UnwiredFlowRefuses(t *testing.T) {
	install.SetFlow(nil)
	_, err := (handlers{}).DispatchIntent(context.Background(), install.IntentName, []byte(`{"intent":"git push"}`))
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("DispatchIntent with no Flow wired: err = %v, want KindUnavailable", err)
	}
}
