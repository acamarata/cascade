package install_test

// Purpose (this file): TestAcceptance_X_AlreadyInstalled -- the §5.9
//   idempotency AC (06-FORGE-SPEC.md). Unlike TestAcceptance_X_LinkGitHub
//   (see acceptance_x_test.go's HONEST GAP header), this path DOES reach
//   a genuine, real, successful resume: plugins.AddPlugin's own
//   already-installed short-circuit (LoadMetadata finds a matching
//   InstalledVersion) fires BEFORE the elevation-required decision is
//   ever evaluated, so a process-tier plugin already on record as
//   installed never touches the elevation broker or the unresolved
//   process-tier trust gate at all.
// SPORT: plugins/cascade-pa/install:acceptance (ADD) -- P1-E24-W5-S50-T7.

import (
	"bytes"
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/internal/plugins/resolver"
	"github.com/acamarata/cascade/pkg/plugin"
	"github.com/acamarata/cascade/plugins/cascade-pa/install"
)

func TestAcceptance_X_AlreadyInstalled(t *testing.T) {
	ctx := context.Background()
	idx := acceptVerifiedIndex(t)
	artifact := acceptRealArtifact(t)
	installer := newAcceptRegistryInstaller(t, acceptVerifier(), artifact)

	m, err := plugin.ParseManifest(bytes.NewReader(artifact))
	if err != nil {
		t.Fatalf("ParseManifest(real cascade-github artifact): %v", err)
	}
	// Pre-record cascade-github as already installed at its current
	// version -- the real bookkeeping a prior successful (elevated) add
	// would have written via plugins.SaveMetadata.
	if err := plugins.SaveMetadata(ctx, installer.store, plugins.PluginMetadata{
		Name: m.ID, InstalledVersion: m.Version, Enabled: true,
		RuntimeMode: m.Runtime, Grants: append([]string(nil), m.Requires...),
	}); err != nil {
		t.Fatalf("SaveMetadata: %v", err)
	}

	confirm := &acceptConfirm{outcome: install.ConfirmYes}
	bus := &acceptBus{}
	f := install.NewFlow(install.Deps{Resolver: resolver.NewIntentResolver(), Confirm: confirm,
		Install: installer, Elevate: acceptWithheldElevator{}, Events: bus})

	for i := 0; i < 2; i++ {
		result, err := f.Run(ctx, install.RunRequest{Intent: acceptIntent, Index: idx})
		if err != nil || !result.Resumed {
			t.Fatalf("run %d: Run() = (%+v, %v), want resumed with no error", i, result, err)
		}
	}
	if len(installer.calls) != 2 {
		t.Fatalf("Installer.Add called %d times, want one attempt per invocation (delta reported each time)", len(installer.calls))
	}
	if bus.has(install.EventElevationRequired) {
		t.Fatal("an already-installed candidate must never require elevation (acceptWithheldElevator would refuse it)")
	}
	last := bus.events[len(bus.events)-1]
	if last.Kind != install.EventConversationResume || last.Resume == nil || !last.Resume.AlreadyInstalled {
		t.Fatalf("last event = %v, want a resume reporting AlreadyInstalled", last)
	}
}
