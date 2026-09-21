package plugins

// Purpose (this file): unit + end-to-end coverage for
//   cascadepa_install_confirm.go's real approvalConfirmGate -- enqueue,
//   decide-approve, decide-deny, token reuse, and expiry, all driven
//   through the REAL internal/policy.ApprovalQueue this gate builds (no
//   fakes for the queue itself; only Confirm's caller-side loop is under
//   test). g.init is called from a second goroutine after Confirm has
//   started -- safe because sync.Once establishes happens-before ordering
//   between concurrent callers, so both goroutines observe the same,
//   fully-initialized *StoreApprovals.
// SPORT: internal/plugins:cascadepa-install-wiring (TEST) -- P1-E24-W5-S50-T4.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/plugins/cascade-pa/install"
)

// confirmResult bundles Confirm's two return values for a channel.
type confirmResult struct {
	outcome install.ConfirmOutcome
	err     error
}

// newTestConfirmGate builds an approvalConfirmGate over a real, host-
// injected ApprovalQueue (hostRPCFixture). Round-3 rework (T0 decision
// D1): this gate has no private-queue fallback left to build against, so
// every test in this file drives the SAME real queue/registry shape the
// daemon's own composition root injects.
func newTestConfirmGate(t *testing.T, clock *testkit.FrozenClock) *approvalConfirmGate {
	t.Helper()
	hostRPCFixture(t, clock)
	g := newApprovalConfirmGate(clock)
	g.poll = time.Millisecond
	return g
}

// awaitPending blocks until the queue has at least one pending entry and
// returns it. Bounded by a real 2s wall-clock timeout so a wiring bug
// fails the test instead of hanging the suite.
func awaitPending(t *testing.T, queue policy.ApprovalQueue) policy.PendingEntry {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		pending, err := queue.GetPending(context.Background())
		if err != nil {
			t.Fatalf("GetPending: %v", err)
		}
		if len(pending) > 0 {
			return pending[0]
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("no pending approval entry appeared within 2s")
	return policy.PendingEntry{}
}

func TestApprovalConfirmGate_EnqueueDecideApproveConfirms(t *testing.T) {
	clock := testkit.NewFrozenClock(fixedInstallTestTime)
	g := newTestConfirmGate(t, clock)

	resCh := make(chan confirmResult, 1)
	go func() {
		out, err := g.Confirm(context.Background(), install.Proposal{PluginID: "git-tools", Version: "1.0.0"})
		resCh <- confirmResult{out, err}
	}()

	queue, err := g.init(context.Background())
	if err != nil {
		t.Fatalf("init: %v", err)
	}
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
			t.Fatalf("Confirm outcome = %v, want ConfirmYes after a real approval", res.outcome)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Confirm did not return after approval")
	}
}

func TestApprovalConfirmGate_DecideDenyDeclines(t *testing.T) {
	clock := testkit.NewFrozenClock(fixedInstallTestTime)
	g := newTestConfirmGate(t, clock)

	resCh := make(chan confirmResult, 1)
	go func() {
		out, err := g.Confirm(context.Background(), install.Proposal{PluginID: "git-tools", Version: "1.0.0"})
		resCh <- confirmResult{out, err}
	}()

	queue, err := g.init(context.Background())
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	entry := awaitPending(t, queue)
	if _, err := queue.Decide(context.Background(), []policy.DecisionRequest{{
		RequestID: entry.RequestID, Approved: false, PresentedSummary: entry.Summary, PresentedLevel: policy.L2,
	}}); err != nil {
		t.Fatalf("Decide: %v", err)
	}

	select {
	case res := <-resCh:
		if res.err != nil {
			t.Fatalf("Confirm: %v, want nil -- a deny is a normal decline, not an infrastructure error", res.err)
		}
		if res.outcome != install.ConfirmDeclined {
			t.Fatalf("Confirm outcome = %v, want ConfirmDeclined after a real denial", res.outcome)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Confirm did not return after denial")
	}
}

// TestApprovalConfirmGate_TokenReuseRefused drives the queue directly
// (bypassing Confirm's own single-consume call) to prove the SAME real
// queue this gate builds refuses a second redemption of an already-spent
// token.
func TestApprovalConfirmGate_TokenReuseRefused(t *testing.T) {
	clock := testkit.NewFrozenClock(fixedInstallTestTime)
	g := newTestConfirmGate(t, clock)
	queue, err := g.init(context.Background())
	if err != nil {
		t.Fatalf("init: %v", err)
	}

	action, params, summary := installApprovalAction(install.Proposal{PluginID: "git-tools", Version: "1.0.0"})
	enq, err := queue.Enqueue(context.Background(), policy.EnqueueRequest{
		Subject: installApprovalSubject, Capability: installApprovalCapability,
		Level: policy.L2, Action: action, Params: params, Summary: summary,
	})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	// PresentedSummary must be the entry's OWN stored summary (enq.Summary,
	// which the queue may render differently from the raw summary this
	// call passed in), never the locally-built string -- a mismatch here
	// silently leaves the entry Pending rather than approving it.
	if _, err := queue.Decide(context.Background(), []policy.DecisionRequest{{
		RequestID: enq.RequestID, Approved: true, PresentedSummary: enq.Summary, PresentedLevel: policy.L2,
	}}); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	consume := policy.ConsumeRequest{RequestID: enq.RequestID, Nonce: enq.Token.Nonce, Action: action, Params: params}
	if _, err := queue.ConsumeToken(context.Background(), consume); err != nil {
		t.Fatalf("first ConsumeToken: %v", err)
	}
	if _, err := queue.ConsumeToken(context.Background(), consume); err == nil {
		t.Fatal("second ConsumeToken: err = nil, want the single-use replay refusal")
	}
}

// TestApprovalConfirmGate_ExpiryRefused advances the injected clock past
// MaxApprovalTTL instead of sleeping in real time: the queue's own expiry
// check reads clock.Now(), so this proves the real Sec5.24 ceiling, not a
// simulated one.
func TestApprovalConfirmGate_ExpiryRefused(t *testing.T) {
	clock := testkit.NewFrozenClock(fixedInstallTestTime)
	g := newTestConfirmGate(t, clock)

	resCh := make(chan confirmResult, 1)
	go func() {
		out, err := g.Confirm(context.Background(), install.Proposal{PluginID: "git-tools", Version: "1.0.0"})
		resCh <- confirmResult{out, err}
	}()

	queue, err := g.init(context.Background())
	if err != nil {
		t.Fatalf("init: %v", err)
	}
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

// TestApprovalConfirmGate_Confirm_NoHostDepsRefuses drives Confirm's own
// g.init(ctx) error branch (not init() called directly) -- round-3 rework
// (T0 decision D1): with no SetInstallHostDeps call, this gate has no
// fallback left to build a queue from, so it refuses with KindUnavailable.
func TestApprovalConfirmGate_Confirm_NoHostDepsRefuses(t *testing.T) {
	SetInstallHostDeps(nil)
	g := newApprovalConfirmGate(testkit.NewFrozenClock(fixedInstallTestTime))
	_, err := g.Confirm(context.Background(), install.Proposal{PluginID: "x", Version: "1.0.0"})
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("Confirm: err = %v, want KindUnavailable", err)
	}
	if !strings.Contains(err.Error(), "SetInstallHostDeps") {
		t.Fatalf("Confirm: err = %v, want it to name the missing SetInstallHostDeps wiring", err)
	}
	if g.queue != nil {
		t.Fatal("g.queue is non-nil after a refused init -- no private queue must ever be built")
	}
}

// TestApprovalConfirmGate_ContextCanceledPropagates drives
// awaitApprovalDecision's ctx.Done() branch: a context that expires before
// any decision is made returns the context's own error, never a fabricated
// outcome.
func TestApprovalConfirmGate_ContextCanceledPropagates(t *testing.T) {
	clock := testkit.NewFrozenClock(fixedInstallTestTime)
	g := newTestConfirmGate(t, clock)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	_, err := g.Confirm(ctx, install.Proposal{PluginID: "git-tools", Version: "1.0.0"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Confirm: err = %v, want context.DeadlineExceeded", err)
	}
}

// TestCapApprovalText_TruncatesLongText drives the actual truncation
// branch (a real over-limit string), not just the pass-through case every
// other test in this file exercises via short fixture names.
func TestCapApprovalText_TruncatesLongText(t *testing.T) {
	long := strings.Repeat("x", approvalTextDisplayLimit+10)
	got := capApprovalText(long)
	if len(got) != approvalTextDisplayLimit {
		t.Fatalf("capApprovalText(len=%d) = len %d, want exactly %d", len(long), len(got), approvalTextDisplayLimit)
	}
}
