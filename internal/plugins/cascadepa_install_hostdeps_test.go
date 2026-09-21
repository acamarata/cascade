package plugins

// Purpose (this file): the round-2 rework proof for T0 decision D1 -- a
//   decision made through the REAL approval RPC handler path
//   (policy.MethodHandlers over the SAME queue InstallHostDeps injects)
//   completes the confirm gate; ConfirmDeclined only on a real deny.
//   Proves two things the round-1 confirming review found false: (1) the
//   gate's queue is the SAME instance a caller with only the injected
//   InstallHostDeps holds, never a second, private one; (2) a decision
//   made through policy.MethodHandlers -- the exact map
//   cmd/cascade/daemon_unix_policy.go's wirePolicy registers for the
//   daemon's own approval.* RPC surface -- reaches this gate's own
//   awaitApprovalDecision loop.
// SPORT: internal/plugins:cascadepa-install-wiring (TEST) -- FIX P1-E24-W5-S50-T4 (D1).

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/plugins/cascade-pa/install"
	sqlitestore "github.com/acamarata/cascade/providers/sqlite"
)

// hostRPCFixture builds a real store/registry/grants/queue trio -- the
// same shape cmd/cascade/daemon_unix_policy.go's wirePolicy builds for the
// daemon's own approval.* RPC surface -- and injects it via
// SetInstallHostDeps, matching cmd/cascade/daemon_unix_cascadepa_install.go's
// real call site. Every cleanup is registered here so this test never
// leaks host-deps state into a sibling test in this package.
func hostRPCFixture(t *testing.T, clock runtime.Clock) (policy.ApprovalQueue, map[string]policy.MethodFunc) {
	t.Helper()
	dir := t.TempDir()
	store, err := sqlitestore.Open(context.Background(), filepath.Join(dir, "cascade.db"))
	if err != nil {
		t.Fatalf("sqlitestore.Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	registry := policy.NewMemoryRegistry()
	grants, err := policy.NewStoreGrants(store, registry, clock)
	if err != nil {
		t.Fatalf("NewStoreGrants: %v", err)
	}
	queue, err := policy.NewApprovalQueue(policy.ApprovalQueueConfig{
		Store: store, Registry: registry, Grants: grants, Clock: clock,
	})
	if err != nil {
		t.Fatalf("NewApprovalQueue: %v", err)
	}

	SetInstallHostDeps(&InstallHostDeps{Store: store, Queue: queue, Registry: registry})
	t.Cleanup(func() { SetInstallHostDeps(nil) })

	handlers := policy.MethodHandlers(policy.RPCDeps{Queue: queue, Registry: registry, Clock: clock})
	return queue, handlers
}

// newHostConfirmGate builds an approvalConfirmGate. Round-3 rework (T0
// decision D1): the gate no longer holds a *sharedCascadeStore at all --
// its only user of one was the deleted private-queue fallback.
func newHostConfirmGate(t *testing.T, clock runtime.Clock) *approvalConfirmGate {
	t.Helper()
	g := newApprovalConfirmGate(clock)
	g.poll = time.Millisecond
	return g
}

// hostStoreFixture opens a real, TempDir-backed provider.Store at
// filepath.Join(dir, "cascade.db") and injects it (Store only) via
// SetInstallHostDeps -- for a caller whose adapter only needs
// shared.open() to succeed (installerAdapter, dbEventBus), sharing the
// SAME cascade.db path its own resolvePaths already points at, matching
// the daemon's real composition root (where the injected Store and the
// adapter's own raw *sql.DB are always opened against the identical
// path). Registers cleanup that closes the store and resets host deps to
// nil, so this test never leaks state into a sibling test in this
// package.
func hostStoreFixture(t *testing.T, dir string) provider.Store {
	t.Helper()
	store, err := sqlitestore.Open(context.Background(), filepath.Join(dir, "cascade.db"))
	if err != nil {
		t.Fatalf("sqlitestore.Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	SetInstallHostDeps(&InstallHostDeps{Store: store})
	t.Cleanup(func() { SetInstallHostDeps(nil) })
	return store
}

// TestApprovalConfirmGate_HostDeps_QueueIsTheSharedInstance is the direct
// proof against the confirming review's Q3 finding: g.init must return the
// EXACT queue InstallHostDeps injected, never a second, private one this
// gate built for itself.
func TestApprovalConfirmGate_HostDeps_QueueIsTheSharedInstance(t *testing.T) {
	clock := testkit.NewFrozenClock(fixedInstallTestTime)
	queue, _ := hostRPCFixture(t, clock)
	g := newHostConfirmGate(t, clock)

	got, err := g.init(context.Background())
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	if got != queue {
		t.Fatal("g.init returned a queue that is not the injected InstallHostDeps.Queue -- the gate built its own private one")
	}
}

// TestApprovalConfirmGate_HostDeps_SharedQueueDecideApproveCompletesGate
// proves a decision made against the shared queue (held only via the
// InstallHostDeps this test injected, never through the gate itself)
// completes Confirm with ConfirmYes.
func TestApprovalConfirmGate_HostDeps_SharedQueueDecideApproveCompletesGate(t *testing.T) {
	clock := testkit.NewFrozenClock(fixedInstallTestTime)
	queue, _ := hostRPCFixture(t, clock)
	g := newHostConfirmGate(t, clock)

	resCh := make(chan confirmResult, 1)
	go func() {
		out, err := g.Confirm(context.Background(), install.Proposal{PluginID: "git-tools", Version: "1.0.0"})
		resCh <- confirmResult{out, err}
	}()

	entry := awaitPending(t, queue)
	if _, err := queue.Decide(context.Background(), []policy.DecisionRequest{{
		RequestID: entry.RequestID, Approved: true, PresentedSummary: entry.Summary, PresentedLevel: policy.L2,
	}}); err != nil {
		t.Fatalf("Decide: %v", err)
	}

	select {
	case res := <-resCh:
		if res.err != nil {
			t.Fatalf("Confirm: %v", res.err)
		}
		if res.outcome != install.ConfirmYes {
			t.Fatalf("Confirm outcome = %v, want ConfirmYes after a decision on the shared queue", res.outcome)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Confirm did not return after approval")
	}
}

// TestApprovalConfirmGate_HostDeps_RealRPCApprovalDenyDeclines drives the
// deny decision through policy.MethodHandlers["approval.deny"] -- the
// literal handler map cmd/cascade/daemon_unix_policy.go's wirePolicy
// registers for the daemon's live approval.* RPC surface (approval.deny is
// L2, non-elevated -- internal/rpc/elevation.go's table -- so no Attestor
// double is needed, matching cmd/cascade's own live wiring for this verb).
// approval.grant (the "approve" redemption verb) is L3/always-elevated AND
// needs a cryptographically signed token from a real ApprovalVerifier;
// exercising it is out of this test's reach without duplicating that whole
// subsystem, so the positive "decide approve" path above still drives
// queue.Decide directly, on the shared queue instance -- the same
// primitive both approval.deny (below) and the engine's own ask-verdict
// path call.
func TestApprovalConfirmGate_HostDeps_RealRPCApprovalDenyDeclines(t *testing.T) {
	clock := testkit.NewFrozenClock(fixedInstallTestTime)
	queue, handlers := hostRPCFixture(t, clock)
	g := newHostConfirmGate(t, clock)

	resCh := make(chan confirmResult, 1)
	go func() {
		out, err := g.Confirm(context.Background(), install.Proposal{PluginID: "git-tools", Version: "1.0.0"})
		resCh <- confirmResult{out, err}
	}()

	entry := awaitPending(t, queue)
	params, err := json.Marshal(policy.ApprovalDecisionParams{
		RequestID: entry.RequestID, PresentedSummary: entry.Summary, PresentedLevel: policy.L2,
	})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	if _, err := handlers["approval.deny"](context.Background(), params); err != nil {
		t.Fatalf("approval.deny: %v", err)
	}

	select {
	case res := <-resCh:
		if res.err != nil {
			t.Fatalf("Confirm: %v, want nil -- a deny is a normal decline", res.err)
		}
		if res.outcome != install.ConfirmDeclined {
			t.Fatalf("Confirm outcome = %v, want ConfirmDeclined after the real approval.deny RPC handler denied it", res.outcome)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Confirm did not return after the RPC deny")
	}
}

// TestApprovalConfirmGate_HostDeps_ExpiryDeclines proves the expiry path
// still declines when the queue is the shared, host-injected instance.
func TestApprovalConfirmGate_HostDeps_ExpiryDeclines(t *testing.T) {
	clock := testkit.NewFrozenClock(fixedInstallTestTime)
	queue, _ := hostRPCFixture(t, clock)
	g := newHostConfirmGate(t, clock)

	resCh := make(chan confirmResult, 1)
	go func() {
		out, err := g.Confirm(context.Background(), install.Proposal{PluginID: "git-tools", Version: "1.0.0"})
		resCh <- confirmResult{out, err}
	}()

	awaitPending(t, queue)
	clock.Advance(policy.MaxApprovalTTL + time.Second)

	select {
	case res := <-resCh:
		if res.err != nil {
			t.Fatalf("Confirm: %v, want nil -- expiry declines, it does not error", res.err)
		}
		if res.outcome != install.ConfirmDeclined {
			t.Fatalf("Confirm outcome = %v, want ConfirmDeclined after expiry", res.outcome)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Confirm did not return after expiry")
	}
}

// TestApprovalConfirmGate_HostDeps_CapabilityRegisteredOnSameRegistry
// proves ensureInstallCapabilityRegistered lands the capability on the
// SAME registry instance the queue's own admissible() check consults --
// an Enqueue that reached a pending entry at all (awaitPending above,
// implicitly) already proves this, but this test asserts it directly via
// Registry.Lookup, and separately proves a second gate sharing the same
// host deps does not error registering the same capability twice.
func TestApprovalConfirmGate_HostDeps_CapabilityRegisteredOnSameRegistry(t *testing.T) {
	clock := testkit.NewFrozenClock(fixedInstallTestTime)
	hostRPCFixture(t, clock)
	g1 := newHostConfirmGate(t, clock)
	g2 := newHostConfirmGate(t, clock)

	if _, err := g1.init(context.Background()); err != nil {
		t.Fatalf("g1.init: %v", err)
	}
	hd := activeInstallHostDeps()
	if hd == nil {
		t.Fatal("activeInstallHostDeps() = nil after SetInstallHostDeps")
	}
	if _, err := hd.Registry.Lookup(context.Background(), installApprovalCapability); err != nil {
		t.Fatalf("Registry.Lookup(%q): %v, want it registered", installApprovalCapability, err)
	}
	// A second gate initializing against the SAME already-registered
	// capability must not fail -- ensureInstallCapabilityRegistered
	// tolerates the KindConflict, per its own doc comment.
	if _, err := g2.init(context.Background()); err != nil {
		t.Fatalf("g2.init: %v, want a second gate sharing the same host deps to succeed", err)
	}
}
