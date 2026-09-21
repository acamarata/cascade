package plugins

// Purpose (this file): unit coverage for cascadepa_install_confirm.go's
//   error-propagation branches that the end-to-end tests in
//   cascadepa_install_confirm_test.go and cascadepa_install_confirm_e2e_test.go
//   never reach (a real ApprovalQueue never fails Enqueue/GetPending under
//   normal operation) -- split into its own file, round-2 rework.
// SPORT: internal/plugins:cascadepa-install-wiring (TEST) -- FIX P1-E24-W5-S50-T4 (D1, D3).

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/plugins/cascade-pa/install"
)

// failingQueueDouble is a policy.ApprovalQueue double: every embedded
// method not overridden panics if called (nil embedded interface), which
// is deliberate -- each test below exercises exactly one failure branch
// and must never reach past it.
type failingQueueDouble struct {
	policy.ApprovalQueue
	enqueueErr    error
	getPendingErr error
}

func (f failingQueueDouble) Enqueue(context.Context, policy.EnqueueRequest) (policy.EnqueueResult, error) {
	return policy.EnqueueResult{}, f.enqueueErr
}

func (f failingQueueDouble) GetPending(context.Context) ([]policy.PendingEntry, error) {
	return nil, f.getPendingErr
}

// TestApprovalConfirmGate_Confirm_EnqueueFailurePropagates drives Confirm's
// own Enqueue-failure branch directly: g.once is pre-fired with a queue
// double so no real store/registry is needed for this error path alone.
func TestApprovalConfirmGate_Confirm_EnqueueFailurePropagates(t *testing.T) {
	wantErr := errors.New("boom: enqueue refused")
	g := &approvalConfirmGate{}
	g.once.Do(func() { g.queue = failingQueueDouble{enqueueErr: wantErr} })

	_, err := g.Confirm(context.Background(), install.Proposal{PluginID: "git-tools", Version: "1.0.0"})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Confirm: err = %v, want it to wrap %v", err, wantErr)
	}
}

// TestAwaitApprovalDecision_GetPendingFailurePropagates drives
// awaitApprovalDecision's GetPending-failure branch directly -- it is a
// free function, so no approvalConfirmGate is needed at all.
func TestAwaitApprovalDecision_GetPendingFailurePropagates(t *testing.T) {
	wantErr := errors.New("boom: get pending refused")
	queue := failingQueueDouble{getPendingErr: wantErr}
	_, err := awaitApprovalDecision(context.Background(), queue, time.Millisecond,
		policy.EnqueueResult{RequestID: "req-1"}, "install:x@1.0.0", nil)
	if !errors.Is(err, wantErr) {
		t.Fatalf("awaitApprovalDecision: err = %v, want it to wrap %v", err, wantErr)
	}
}

// failingRegistryDouble is a policy.CapabilityRegistry double whose Add
// always returns err -- used to drive ensureInstallCapabilityRegistered's
// (and its callers') non-conflict refusal branch, which a real, freshly
// injected MemoryRegistry never exercises in practice (Add on a fresh
// registry with a valid Capability never fails).
type failingRegistryDouble struct {
	policy.CapabilityRegistry
	err error
}

func (f failingRegistryDouble) Add(context.Context, policy.Capability) error { return f.err }

// dummyApprovalQueue is a non-nil-but-otherwise-unused policy.ApprovalQueue
// value: InstallHostDeps.Queue only needs to be non-nil for init's
// host-branch condition to take effect -- the capability registration
// error below returns before Queue itself is ever used.
type dummyApprovalQueue struct{ policy.ApprovalQueue }

// TestApprovalConfirmGate_Init_HostCapabilityRegistrationFailurePropagates
// drives init's host-deps branch when ensureInstallCapabilityRegistered
// fails for a reason OTHER than "already registered" (KindConflict) --
// both the caller's own check in init and ensureInstallCapabilityRegistered's
// own return.
//
// REWORK (round-3, T0 decision D3, FLAG 4): round-2 asserted this with
// errors.Is against a *cascade.Error sentinel -- but *cascade.Error.Is
// compares Kind ONLY (pkg/cascade/errors.go), so ANY KindInvalidInput
// error would have satisfied this assertion, not specifically the one
// failingRegistryDouble returned. wantErr is now a plain errors.New
// sentinel: stdlib errors.Is on a plain error falls back to identity
// (==), so this now proves the EXACT error init returned is the one this
// test's own double produced, not merely one of the same Kind.
func TestApprovalConfirmGate_Init_HostCapabilityRegistrationFailurePropagates(t *testing.T) {
	wantErr := errors.New("boom: capability rejected")
	SetInstallHostDeps(&InstallHostDeps{Queue: dummyApprovalQueue{}, Registry: failingRegistryDouble{err: wantErr}})
	t.Cleanup(func() { SetInstallHostDeps(nil) })

	g := newApprovalConfirmGate(testkit.NewFrozenClock(fixedInstallTestTime))
	_, err := g.init(context.Background())
	if !errors.Is(err, wantErr) {
		t.Fatalf("init: err = %v, want it to wrap the exact sentinel %v (identity, not just Kind)", err, wantErr)
	}
}
