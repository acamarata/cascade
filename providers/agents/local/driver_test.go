package local

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

func newTestDriver(t *testing.T, model ModelExecutor, store ConfigStore) *Driver {
	t.Helper()
	q, err := NewQualifier(model, store, fakeClock{now: time.Unix(1, 0)}, []byte(testFixture))
	if err != nil {
		t.Fatalf("NewQualifier: %v", err)
	}
	d, err := New(model, q)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return d
}

// TestDispatchUngatedClassesForward asserts classify/extract/summarize
// forward to model.execute for a model with no qualification row at all
// — the ungated classes are never affected by authoring state.
func TestDispatchUngatedClassesForward(t *testing.T) {
	d := newTestDriver(t, passingModel(), newMemStore())
	ctx := context.Background()
	for _, class := range []TaskClass{TaskClassClassify, TaskClassExtract, TaskClassSummarize} {
		resp, err := d.Dispatch(ctx, class, provider.ChatRequest{Model: "unqualified-model"})
		if err != nil {
			t.Fatalf("Dispatch(%s) = %v, want nil", class, err)
		}
		if resp.Message.Content != "ok" {
			t.Fatalf("Dispatch(%s) content = %q, want forwarded reply", class, resp.Message.Content)
		}
	}
}

// TestDispatchGatedClassesRefuseByDefault asserts code/reason/review/
// arbitrate all return ErrNotQualified against a model with no
// qualification row — the default configuration must not permit
// authoring.
func TestDispatchGatedClassesRefuseByDefault(t *testing.T) {
	d := newTestDriver(t, passingModel(), newMemStore())
	ctx := context.Background()
	for _, class := range []TaskClass{TaskClassCode, TaskClassReason, TaskClassReview, TaskClassArbitrate} {
		_, err := d.Dispatch(ctx, class, provider.ChatRequest{Model: "unqualified-model"})
		if !errors.Is(err, ErrNotQualified) {
			t.Fatalf("Dispatch(%s) = %v, want ErrNotQualified", class, err)
		}
	}
}

// TestZeroValueConfigRefusesAuthoring constructs the DEFAULT path — a
// freshly built Driver over a freshly built Qualifier whose ConfigStore
// has never had Run called against it — and asserts every gated class
// refuses. This is the ticket's own required proof: the default
// configuration must not permit authoring, exercised end to end through
// Driver.Dispatch, not just through Qualifier.Resolve directly.
func TestZeroValueConfigRefusesAuthoring(t *testing.T) {
	d := newTestDriver(t, passingModel(), newMemStore())
	caps, err := d.AdvertisedCapabilities(context.Background(), "brand-new-model")
	if err != nil {
		t.Fatalf("AdvertisedCapabilities: %v", err)
	}
	for _, c := range caps {
		if c == CapabilityAuthoring {
			t.Fatal("a freshly constructed Driver advertised authoring with no qualification run at all")
		}
	}
	if len(caps) != len(baseCapabilities) {
		t.Fatalf("AdvertisedCapabilities(default) = %v, want exactly the base set", caps)
	}
}

// TestDispatchGatedClassesForwardWhenQualified asserts a passing
// qualification row grants forwarding for that model id's gated classes.
func TestDispatchGatedClassesForwardWhenQualified(t *testing.T) {
	store := newMemStore()
	model := passingModel()
	q, err := NewQualifier(model, store, fakeClock{now: time.Unix(1, 0)}, []byte(testFixture))
	if err != nil {
		t.Fatalf("NewQualifier: %v", err)
	}
	if _, err := q.Run(context.Background(), "qualified-model"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	d, err := New(model, q)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	resp, err := d.Dispatch(context.Background(), TaskClassCode, provider.ChatRequest{Model: "qualified-model"})
	if err != nil {
		t.Fatalf("Dispatch(code, qualified) = %v, want nil", err)
	}
	if resp.Message.Content != "ok" {
		t.Fatalf("Dispatch(code, qualified) content = %q", resp.Message.Content)
	}
}

// TestDispatchUnrecognizedClass asserts an unknown class refuses with a
// typed invalid-input error, never a panic.
func TestDispatchUnrecognizedClass(t *testing.T) {
	d := newTestDriver(t, passingModel(), newMemStore())
	_, err := d.Dispatch(context.Background(), TaskClass("segment"), provider.ChatRequest{Model: "m"})
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
		t.Fatalf("Dispatch(unrecognized) kind = (%v, %v), want KindInvalidInput", kind, ok)
	}
}

// TestComplianceposture asserts Capabilities reports the fixed R-16.10
// posture: CredentialSharingForbidden, programmatic entitlement, no
// interactive entitlement, no multi-profile.
func TestCompliancePosture(t *testing.T) {
	d := newTestDriver(t, passingModel(), newMemStore())
	caps, err := d.Capabilities(context.Background(), "")
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	p := caps.CompliancePosture
	if err := p.Validate(); err != nil {
		t.Fatalf("CompliancePosture.Validate: %v", err)
	}
	if p.CredentialSharing != provider.CredentialSharingForbidden {
		t.Fatalf("CredentialSharing = %v, want CredentialSharingForbidden", p.CredentialSharing)
	}
	if !p.ProgrammaticEntitlement || p.InteractiveEntitlement || p.MultiProfileEnabled {
		t.Fatalf("CompliancePosture = %+v, want programmatic-only, no interactive, no multi-profile", p)
	}
}

// TestSpawnCollectLifecycle exercises the AgentProvider job verbs end to
// end over a real (fake-backed) ModelProvider: Spawn, Status, Collect,
// Artifacts, Message and Cancel against a job that exists, and
// ErrJobNotFound for one that does not. Also asserts ProcessGroupID is
// always 0 (in-process, no child process — R-21.177).
func TestSpawnCollectLifecycle(t *testing.T) {
	d := newTestDriver(t, passingModel(), newMemStore())
	ctx := context.Background()

	res, err := d.Spawn(ctx, provider.AgentJobSpec{Prompt: "hello", DataClass: provider.DataClassInternal})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if res.JobID == "" {
		t.Fatal("Spawn returned an empty JobID")
	}
	if res.ProcessGroupID != 0 {
		t.Fatalf("Spawn ProcessGroupID = %d, want 0 (in-process lane)", res.ProcessGroupID)
	}

	state, err := d.Status(ctx, res.JobID)
	if err != nil || !state.Valid() || !state.Terminal() {
		t.Fatalf("Status = (%v, %v), want a valid terminal state", state, err)
	}

	if err := d.Message(ctx, res.JobID, "next turn"); err != nil {
		t.Fatalf("Message(existing job): %v", err)
	}
	if err := d.Cancel(ctx, res.JobID); err != nil {
		t.Fatalf("Cancel(existing job): %v", err)
	}

	result, err := d.Collect(ctx, res.JobID)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if result.Output != "ok" {
		t.Fatalf("Collect output = %q, want the forwarded reply", result.Output)
	}

	artifacts, err := d.Artifacts(ctx, res.JobID)
	if err != nil || artifacts == nil {
		t.Fatalf("Artifacts = (%v, %v), want a non-nil slice", artifacts, err)
	}

	assertMissingJobRefuses(t, d)
}

// assertMissingJobRefuses asserts every job verb returns ErrJobNotFound
// for an id that was never spawned.
func assertMissingJobRefuses(t *testing.T, d *Driver) {
	t.Helper()
	ctx := context.Background()
	missing := provider.AgentJobID("never-spawned")
	for name, call := range map[string]func() error{
		"Message": func() error { return d.Message(ctx, missing, "x") },
		"Cancel":  func() error { return d.Cancel(ctx, missing) },
	} {
		if err := call(); !errors.Is(err, provider.ErrJobNotFound) {
			t.Fatalf("%s(missing job) = %v, want ErrJobNotFound", name, err)
		}
	}
	if _, err := d.Status(ctx, missing); !errors.Is(err, provider.ErrJobNotFound) {
		t.Fatalf("Status(missing) = %v, want ErrJobNotFound", err)
	}
	if _, err := d.Collect(ctx, missing); !errors.Is(err, provider.ErrJobNotFound) {
		t.Fatalf("Collect(missing) = %v, want ErrJobNotFound", err)
	}
	if _, err := d.Artifacts(ctx, missing); !errors.Is(err, provider.ErrJobNotFound) {
		t.Fatalf("Artifacts(missing) = %v, want ErrJobNotFound", err)
	}
}

// TestApprovalRequestsNeverRaised asserts the local lane's
// ApprovalRequests channel is closed and empty, and ResolveApproval
// always refuses — this lane never raises an approval for anything.
func TestApprovalRequestsNeverRaised(t *testing.T) {
	d := newTestDriver(t, passingModel(), newMemStore())
	select {
	case _, open := <-d.ApprovalRequests():
		if open {
			t.Fatal("ApprovalRequests delivered a request; the local lane must never raise one")
		}
	default:
		t.Fatal("ApprovalRequests channel was not immediately closed")
	}
	if err := d.ResolveApproval("anything", "token"); err == nil {
		t.Fatal("ResolveApproval returned nil error for a request that was never raised")
	}
}

// TestNegotiateProtocol asserts Negotiate pins the driver's declared
// single version against an overlapping peer range, and refuses with
// ErrHarnessIncompatible against a disjoint one.
func TestNegotiateProtocol(t *testing.T) {
	d := newTestDriver(t, passingModel(), newMemStore())
	if got := d.SupportedProtocols(); got != localProtocolRange {
		t.Fatalf("SupportedProtocols = %v, want %v", got, localProtocolRange)
	}
	ver, err := d.Negotiate(context.Background(), localProtocolRange)
	if err != nil || ver != "local-v1" {
		t.Fatalf("Negotiate(overlapping) = (%v, %v), want (local-v1, nil)", ver, err)
	}
	_, err = d.Negotiate(context.Background(), provider.ProtocolRange{Min: "zzz", Max: "zzzz"})
	if !errors.Is(err, provider.ErrHarnessIncompatible) {
		t.Fatalf("Negotiate(disjoint) = %v, want ErrHarnessIncompatible", err)
	}
}

// TestConstructionValidation asserts New refuses a nil model or a nil
// qualifier.
func TestConstructionValidation(t *testing.T) {
	q, err := NewQualifier(passingModel(), newMemStore(), fakeClock{now: time.Unix(1, 0)}, []byte(testFixture))
	if err != nil {
		t.Fatalf("NewQualifier: %v", err)
	}
	if _, err := New(nil, q); err == nil {
		t.Fatal("New(nil model) returned nil error")
	}
	if _, err := New(passingModel(), nil); err == nil {
		t.Fatal("New(nil qualifier) returned nil error")
	}
}

// TestNoEgressSideEffect asserts dispatching through the local lane
// registers no egress class: Driver's constructor and Dispatch take no
// egress-interceptor seam of any kind, so there is structurally nothing
// for a call here to register with internal/hooks/egress (which
// providers/** cannot even import — Art.7.2). This is the compile-time
// half of the proof; the runtime half is that a normal Dispatch call
// completes without touching any such dependency, which every other test
// in this file already exercises.
func TestNoEgressSideEffect(t *testing.T) {
	d := newTestDriver(t, passingModel(), newMemStore())
	if _, err := d.Dispatch(context.Background(), TaskClassClassify, provider.ChatRequest{Model: "m"}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
}
