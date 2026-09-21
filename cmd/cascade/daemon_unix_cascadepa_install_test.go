//go:build !windows

// Purpose: unit coverage for wireCascadePAInstallHostDeps -- proves the
//
//	composition-root call constructs the exact *plugins.InstallHostDeps its
//	caller passed in and hands it to internal/plugins.SetInstallHostDeps.
//
// REWORK (round-3, T0 decision D2): round-2 proved this via
//
//	internal/plugins.InstallHostDepsConfigured, a boolean probe with no
//	production caller anywhere in the tree -- internal/build's
//	TestTestOnlyUsage_RealTreeGreen gate correctly refuses a test-only
//	exported symbol like that. wireCascadePAInstallHostDeps now RETURNS the
//	*plugins.InstallHostDeps it built, so this test asserts on the returned
//	value's three exported fields directly -- no new exported symbol in
//	internal/plugins, no allow-list entry.
//
// SPORT: cmd/cascade:cascadepa-install-hostdeps (TEST) -- FIX P1-E24-W5-S50-T4 (D2).
package main

import (
	"testing"

	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/pkg/provider"
)

// fakeInstallHostStore is a minimal provider.Store double, only used for
// identity/non-nil proof -- this test never calls any of its methods.
type fakeInstallHostStore struct{ provider.Store }

// fakeInstallHostQueue is a minimal policy.ApprovalQueue double, likewise.
type fakeInstallHostQueue struct{ policy.ApprovalQueue }

// fakeInstallHostRegistry is a minimal policy.CapabilityRegistry double,
// likewise.
type fakeInstallHostRegistry struct{ policy.CapabilityRegistry }

// TestWireCascadePAInstallHostDeps_ReturnsInjectedDeps proves
// platformDaemonRun's one-line call constructs the exact struct its own
// caller passed in: the returned *plugins.InstallHostDeps' Store/Queue/
// Registry fields are the SAME interface values this test handed the call,
// by identity. Whether SetInstallHostDeps was invoked cannot be proven from
// package main under D2 (no exported probe exists by design); the three-line
// function body is the only guard, and this test covers it.
func TestWireCascadePAInstallHostDeps_ReturnsInjectedDeps(t *testing.T) {
	plugins.SetInstallHostDeps(nil)
	t.Cleanup(func() { plugins.SetInstallHostDeps(nil) })

	wantStore := fakeInstallHostStore{}
	wantQueue := fakeInstallHostQueue{}
	wantRegistry := fakeInstallHostRegistry{}

	got := wireCascadePAInstallHostDeps(wantStore, wantQueue, wantRegistry)
	if got == nil {
		t.Fatal("wireCascadePAInstallHostDeps returned nil, want a non-nil *plugins.InstallHostDeps")
	}
	if got.Store != provider.Store(wantStore) {
		t.Errorf("returned deps.Store = %#v, want the exact store this call was given", got.Store)
	}
	if got.Queue != policy.ApprovalQueue(wantQueue) {
		t.Errorf("returned deps.Queue = %#v, want the exact queue this call was given", got.Queue)
	}
	if got.Registry != policy.CapabilityRegistry(wantRegistry) {
		t.Errorf("returned deps.Registry = %#v, want the exact registry this call was given", got.Registry)
	}
}
