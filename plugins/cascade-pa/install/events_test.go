package install

// Purpose (this file): the EventBus injection seam (fail-closed default,
//   SetEventBus/reset) and buildProposal's value-free rendering of both
//   candidate sources (flow.go).
// SPORT: plugins/cascade-pa/install (ADD) -- P1-E24-W5-S50-T4.

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
)

func TestUnconfiguredEventBus_FailsClosed(t *testing.T) {
	SetEventBus(nil)
	err := activeEventBus().Publish(context.Background(), Event{Kind: EventInstallProposal})
	if err == nil {
		t.Fatal("Publish() = nil error on the unconfigured default, want a refusal")
	}
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("Publish() error = %v, want KindUnavailable", err)
	}
}

func TestSetEventBus_InjectsAndResets(t *testing.T) {
	fake := &fakeBus{}
	SetEventBus(fake)
	t.Cleanup(func() { SetEventBus(nil) })

	if err := activeEventBus().Publish(context.Background(), Event{Kind: EventInstallProposal}); err != nil {
		t.Fatalf("Publish() error = %v, want nil against the injected fake", err)
	}
	if len(fake.events) != 1 {
		t.Fatalf("fake.events = %v, want exactly one published event", fake.events)
	}

	SetEventBus(nil)
	if err := activeEventBus().Publish(context.Background(), Event{}); err == nil {
		t.Fatal("Publish() after SetEventBus(nil) = nil error, want the unconfigured default restored")
	}
}

func TestBuildProposal_InstalledCandidate(t *testing.T) {
	cand := newCandidate("git-tools", plugin.RuntimeProcess)
	p := buildProposal("git push", cand)

	if p.PluginID != "git-tools" || p.Version != "1.0.0" || p.Runtime != plugin.RuntimeProcess {
		t.Fatalf("buildProposal() = %+v, want the installed manifest's own id/version/runtime", p)
	}
	if len(p.Permissions) != 1 || p.Permissions[0] != "read" {
		t.Fatalf("buildProposal().Permissions = %v, want the manifest's Requires copied through", p.Permissions)
	}
}

func TestBuildProposal_RegistryCandidate(t *testing.T) {
	cand := plugin.Candidate{
		PluginID:      "linter",
		Name:          "Linter",
		Source:        plugin.CandidateSourceRegistry,
		RegistryEntry: plugin.RegistryIndexEntry{ID: "linter", Name: "Linter", LatestVersion: "2.3.0"},
	}
	p := buildProposal("lint this", cand)

	if p.Version != "2.3.0" {
		t.Fatalf("buildProposal().Version = %q, want the registry entry's LatestVersion", p.Version)
	}
	if p.Runtime != "" || p.Permissions != nil {
		t.Fatalf("buildProposal() = %+v, want a zero Runtime/Permissions (no manifest fetched yet)", p)
	}
}

func TestNoInput(t *testing.T) {
	if noInput(nil) {
		t.Fatal("noInput(nil) = true, want false (unset reads as interactive)")
	}
	set1 := func(string) string { return "1" }
	if !noInput(set1) {
		t.Fatal("noInput(getenv-returning-1) = false, want true")
	}
	setOther := func(string) string { return "0" }
	if noInput(setOther) {
		t.Fatal("noInput(getenv-returning-0) = true, want false")
	}
}
