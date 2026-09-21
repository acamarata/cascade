package plugins

// Purpose (this file): the two full-Flow end-to-end tests for the real
//   approvalConfirmGate, split out of cascadepa_install_confirm_test.go to
//   keep that file under the 300-line cap: "enqueue -> decide approve ->
//   install" and "decide deny -> nothing installed", both driving
//   plugins/cascade-pa/install.Flow (not just the gate) through the same
//   real ApprovalQueue cascadepa_install_confirm_test.go's unit tests
//   exercise directly.
// SPORT: internal/plugins:cascadepa-install-wiring (TEST) -- P1-E24-W5-S50-T4.

import (
	"context"
	"crypto/ed25519"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/plugins/resolver"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/plugin"
	"github.com/acamarata/cascade/plugins/cascade-pa/install"
)

// countingInstaller is a local install.Installer double so
// TestEndToEnd_RealApprovalGate_DecideDenyInstallsNothing can assert on
// call count without importing package install's own unexported fakes
// (they live in a different package).
type countingInstaller struct{ calls int }

func (c *countingInstaller) Add(context.Context, install.AddRequest) (install.AddResult, error) {
	c.calls++
	return install.AddResult{Outcome: install.AddOutcomeInstalled}, nil
}

// runResult bundles Flow.Run's two return values for a channel.
type runResult struct {
	res install.RunResult
	err error
}

// decideFirstPending waits for confirm's queue to show one pending entry
// and records approved/denied against it -- the shared second half of
// both tests below.
func decideFirstPending(t *testing.T, confirm *approvalConfirmGate, approved bool) {
	t.Helper()
	queue, err := confirm.init(context.Background())
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	entry := awaitPending(t, queue)
	if _, err := queue.Decide(context.Background(), []policy.DecisionRequest{{
		RequestID: entry.RequestID, Approved: approved, PresentedSummary: entry.Summary, PresentedLevel: policy.L2,
	}}); err != nil {
		t.Fatalf("Decide: %v", err)
	}
}

// awaitRunResult blocks for f.Run's result, bounded by a real 2s timeout
// so a wiring bug fails the test instead of hanging the suite.
func awaitRunResult(t *testing.T, resCh chan runResult) runResult {
	t.Helper()
	select {
	case out := <-resCh:
		return out
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not complete in time")
	}
	return runResult{}
}

// TestEndToEnd_RealApprovalGate_DecideApproveInstalls proves "enqueue ->
// decide approve -> install" through the REAL approvalConfirmGate, driving
// the whole conversational install Flow, not just the gate in isolation.
func TestEndToEnd_RealApprovalGate_DecideApproveInstalls(t *testing.T) {
	dir := t.TempDir()
	resolvePaths := func() (runtime.PathProvider, error) { return tempDataPathProvider{dir: dir}, nil }
	clock := testkit.NewFrozenClock(fixedInstallTestTime)
	// Round-3 rework (T0 decision D1): the confirm gate and the shared
	// store both refuse without injected host deps now -- hostRPCFixture
	// injects a real Store (for the installer's own shared.open()) and a
	// real Queue/Registry (for the confirm gate) in one call.
	hostRPCFixture(t, clock)
	shared := newSharedCascadeStore()

	raw, checksum, signature, pub := signedFixture(t, noRequiresManifest)
	installer := newInstallerAdapter(resolvePaths, clock, shared)
	t.Cleanup(func() { _ = installer.Close() })
	installer.fetcher = func(string) plugin.RegistryFetcher { return &fakeRegistryFetcher{artifact: raw} }
	installer.registryURL = func() (string, error) { return "https://registry.example/", nil }
	installer.registryPubKey = func() (ed25519.PublicKey, error) { return pub, nil }

	confirm := newApprovalConfirmGate(clock)
	confirm.poll = time.Millisecond
	elevator := newInstallElevator(resolvePaths, clock, func(string) string { return "" })
	f := install.NewFlow(install.Deps{
		Resolver: resolver.NewIntentResolver(), Confirm: confirm, Install: installer,
		Elevate: elevator, Events: newBusEventPublisher(&recordingEventPublisher{}), Getenv: func(string) string { return "" },
	})

	resCh := make(chan runResult, 1)
	go func() {
		res, err := f.Run(context.Background(), install.RunRequest{
			Intent: endToEndIntent,
			Index: signedTestIndex(t, []plugin.RegistryIndexEntry{{
				ID: "no-requires-plugin", Name: "No Requires Plugin", Tags: []string{endToEndIntent},
				LatestVersion: "1.0.0",
				Versions:      []plugin.RegistryVersionEntry{{Version: "1.0.0", Checksum: checksum, Signature: signature}},
			}}),
		})
		resCh <- runResult{res, err}
	}()

	decideFirstPending(t, confirm, true)
	out := awaitRunResult(t, resCh)
	if out.err != nil {
		t.Fatalf("Run: %v", out.err)
	}
	if !out.res.Resumed {
		t.Fatal("Resumed = false, want a real approval to complete the install")
	}
}

// TestEndToEnd_RealApprovalGate_DecideDenyInstallsNothing proves "decide
// deny -> nothing installed" through the real gate and Flow together.
func TestEndToEnd_RealApprovalGate_DecideDenyInstallsNothing(t *testing.T) {
	dir := t.TempDir()
	resolvePaths := func() (runtime.PathProvider, error) { return tempDataPathProvider{dir: dir}, nil }
	clock := testkit.NewFrozenClock(fixedInstallTestTime)
	// This test never drives the real installer, only the confirm gate
	// itself -- it needs a Queue/Registry, not a Store; the elevator
	// below still needs a resolvePaths of its own.
	hostRPCFixture(t, clock)

	confirm := newApprovalConfirmGate(clock)
	confirm.poll = time.Millisecond
	installerFake := &countingInstaller{}
	elevator := newInstallElevator(resolvePaths, clock, func(string) string { return "" })
	f := install.NewFlow(install.Deps{
		Resolver: resolver.NewIntentResolver(), Confirm: confirm, Install: installerFake,
		Elevate: elevator, Events: newBusEventPublisher(&recordingEventPublisher{}), Getenv: func(string) string { return "" },
	})

	installed := plugin.ManifestSet{{Enabled: true, Manifest: plugin.Manifest{
		ID: "git-tools", Name: "git-tools", Version: "1.0.0", Runtime: plugin.RuntimeBuiltin,
		Provides: plugin.Provides{Intents: []plugin.IntentSpec{{Name: "git push"}}},
	}}}

	resCh := make(chan runResult, 1)
	go func() {
		res, err := f.Run(context.Background(), install.RunRequest{Intent: "git push", Installed: installed})
		resCh <- runResult{res, err}
	}()

	decideFirstPending(t, confirm, false)
	out := awaitRunResult(t, resCh)
	if out.err != nil {
		t.Fatalf("Run: %v", out.err)
	}
	if out.res.Resumed {
		t.Fatal("Resumed = true after a denial")
	}
	if installerFake.calls != 0 {
		t.Fatalf("Installer.Add called %d times after a denial, want 0 (no side effect)", installerFake.calls)
	}
}
