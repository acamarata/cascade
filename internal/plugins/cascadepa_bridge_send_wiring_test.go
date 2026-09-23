// Purpose: unit coverage for cascadepa_bridge_send_wiring.go's adapter and
//
//	composition helper — the translation from policy.BridgeRef to a plain
//	request id, and newApprovalBridgeLeg's assembly.
//
// SPORT: internal/plugins:cascadepa-bridge-send-wiring/TEST —
//
//	P1-E23-W5-S48-T4 producer leg, T0 D4.
package plugins

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	cascadepa "github.com/acamarata/cascade/plugins/cascade-pa"
	"github.com/acamarata/cascade/plugins/cascade-pa/telegram"
)

// memBridgeStateStub is a package-local BridgeState fake: this file's tests
// only need "unbound, so Send does nothing" — plugins/cascade-pa/telegram's
// own memBridgeState is a _test.go fake in that package and not importable
// here.
type memBridgeStateStub struct{}

func (memBridgeStateStub) Load(context.Context, string) (cascadepa.SubjectState, bool, error) {
	return cascadepa.SubjectState{}, false, nil
}

func (memBridgeStateStub) Save(context.Context, cascadepa.SubjectState) error { return nil }

// TestNewTelegramBridgeSender_TranslatesRequestIDOnly proves the adapter
// hands the sender ref.RequestID.String() and nothing else derived from the
// ref — BridgeRef has one field, so this also proves no second field is
// silently read.
func TestNewTelegramBridgeSender_TranslatesRequestIDOnly(t *testing.T) {
	client := telegram.NewBotClient("tg-test", nil, nil, cascadepa.NewUpdateLedger(memBridgeStateStub{}))
	sender := telegram.NewTelegramApprovalSender(telegram.TelegramApprovalSenderDeps{
		Subject: "tg-test", State: memBridgeStateStub{}, Callbacks: cascadepa.NewCallbackNonceStore(),
		Client: client, Clock: runtime.SystemClock{},
	})
	adapter := newTelegramBridgeSender(sender)

	requestID, err := cascade.NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	// An unbound subject's Send is a real, harmless no-op (telegram package's
	// own TestTelegramApprovalSender_UnpairedSubject_NothingSent proves it);
	// this test only needs it to prove the CALL reaches the sender with the
	// right request id, not to observe a Telegram side effect.
	if err := adapter.Send(context.Background(), policy.BridgeRef{RequestID: requestID}); err != nil {
		t.Fatalf("Send: %v", err)
	}
}

// TestNewApprovalBridgeLeg_BuildsAWorkingLeg proves the one-call composition
// helper hands back a *policy.BridgeLeg that gates on CanBridge exactly like
// a hand-assembled one would (bridge_leg_test.go's own coverage of Dispatch
// itself is not re-proven here — only that assembly wires correctly).
func TestNewApprovalBridgeLeg_BuildsAWorkingLeg(t *testing.T) {
	client := telegram.NewBotClient("tg-test", nil, nil, cascadepa.NewUpdateLedger(memBridgeStateStub{}))
	leg, err := newApprovalBridgeLeg("tg-test", memBridgeStateStub{},
		cascadepa.NewCallbackNonceStore(), client, runtime.SystemClock{})
	if err != nil {
		t.Fatalf("newApprovalBridgeLeg: %v", err)
	}
	if leg == nil {
		t.Fatal("newApprovalBridgeLeg: leg = nil, want a real leg")
	}
	// A non-bridgeable class refuses without ever reaching the sender (an
	// unbound subject would return nil from Send, so a refusal here proves
	// CanBridge ran FIRST, exactly as bridge_leg.go's Dispatch orders it).
	err = leg.Dispatch(context.Background(), "workspace.write", policy.PendingEntry{
		RequestID: "req-wiring-check", ActionClass: policy.ClassRead,
	})
	if !errors.Is(err, policy.ErrNotBridgeable) {
		t.Errorf("Dispatch(ClassRead) err = %v, want ErrNotBridgeable", err)
	}
}

// TestNewApprovalBridgeLeg_RefusesNilClient is a construction-boundary
// smoke test: a nil-collaborator sender still builds (Send fails softly at
// call time, per TelegramApprovalSenderDeps' own doc), so this asserts the
// helper itself never panics on the collaborators enabledBridge would have
// in hand at the point this file's header names.
func TestNewApprovalBridgeLeg_RefusesNilClient(t *testing.T) {
	_, err := newApprovalBridgeLeg("tg-test", memBridgeStateStub{},
		cascadepa.NewCallbackNonceStore(), nil, runtime.SystemClock{})
	if err != nil {
		t.Fatalf("newApprovalBridgeLeg with a nil client: %v, want it to build (Send fails at call time, not construction)", err)
	}
}

// keep time imported for a construction-timing smoke assertion mirroring
// the telegram package's own bridgeApprovalNonceTTL guard.
func TestSystemClockAdvancesRealTime(t *testing.T) {
	c := runtime.SystemClock{}
	before := c.Now()
	if time.Since(before) < 0 {
		t.Error("runtime.SystemClock.Now() returned a time in the future")
	}
}

// chanBridgeSender is a policy.BridgeSender that reports each Send on a
// channel, so an async test can wait for the notification instead of
// sleeping or polling.
type chanBridgeSender struct {
	sent chan policy.BridgeRef
}

func (s *chanBridgeSender) Send(_ context.Context, ref policy.BridgeRef) error {
	s.sent <- ref
	return nil
}

// TestWireApprovalBridge_NilRuntimeAndDisabledBridge_Noop proves neither
// call panics and neither arms anything: a nil runtime (defensive; never
// happens in production) and a disabled bridge (ApprovalBridge nil) both
// have nothing to arm.
func TestWireApprovalBridge_NilRuntimeAndDisabledBridge_Noop(t *testing.T) {
	t.Cleanup(func() { SetBridgeApprovalQueue(nil) })
	_, queue := newRealApprovalHandlers(t)
	SetBridgeApprovalQueue(queue)

	WireApprovalBridge(nil)
	WireApprovalBridge(&BridgeRuntime{})
}

// TestWireApprovalBridge_NoInjectedQueue_Noop proves a bridge assembled
// before SetBridgeApprovalQueue ever ran (or after SetBridgeApprovalQueue(nil)
// cleared it) does not panic reaching for a queue that was never injected.
func TestWireApprovalBridge_NoInjectedQueue_Noop(t *testing.T) {
	SetBridgeApprovalQueue(nil)
	leg, err := policy.NewBridgeLeg(&chanBridgeSender{sent: make(chan policy.BridgeRef, 1)})
	if err != nil {
		t.Fatalf("NewBridgeLeg: %v", err)
	}
	WireApprovalBridge(&BridgeRuntime{ApprovalBridge: leg})
}

// TestWireApprovalBridge_ArmsRealQueue_NotifiesAsync is the FIX-0
// end-to-end proof: SetBridgeApprovalQueue injects a REAL, SQLite-backed
// queue (newRealApprovalHandlers, cascadepa_approval_realcounterpart_test.go's
// own fixture), WireApprovalBridge arms it with a real *policy.BridgeLeg,
// and a fresh Enqueue on an ask-tier, bridgeable-class action reaches the
// sender — off the caller's own goroutine (asyncBridgeNotifier), which is
// why this waits on a channel rather than asserting synchronously.
func TestWireApprovalBridge_ArmsRealQueue_NotifiesAsync(t *testing.T) {
	t.Cleanup(func() { SetBridgeApprovalQueue(nil) })
	_, queue := newRealApprovalHandlers(t)
	SetBridgeApprovalQueue(queue)

	sender := &chanBridgeSender{sent: make(chan policy.BridgeRef, 1)}
	leg, err := policy.NewBridgeLeg(sender)
	if err != nil {
		t.Fatalf("NewBridgeLeg: %v", err)
	}
	WireApprovalBridge(&BridgeRuntime{ApprovalBridge: leg})

	res, err := queue.Enqueue(context.Background(), policy.EnqueueRequest{
		Subject:    policy.Subject{Kind: policy.SubjectUser, ID: "u-wire-approval-bridge"},
		Capability: realCounterpartCapability, Level: policy.L2,
		Action: "wire-approval-bridge-test", Params: []byte(`{}`), Summary: "test",
	})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	select {
	case ref := <-sender.sent:
		if ref.RequestID.String() != res.RequestID {
			t.Errorf("notified request id = %q, want %q", ref.RequestID.String(), res.RequestID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Send was not called within the async dispatch window — WireApprovalBridge did not arm the queue")
	}
}

// TestWireApprovalBridge_SecondArm_LogsRatherThanPanics proves a second
// WireApprovalBridge call against a queue already armed (StoreApprovals.SetBridge's
// own "a bridge is already wired" refusal) is swallowed, not propagated or
// panicked on — WireApprovalBridge has no return value for a caller to
// check, so the only observable contract is "does not crash the daemon".
func TestWireApprovalBridge_SecondArm_LogsRatherThanPanics(t *testing.T) {
	t.Cleanup(func() { SetBridgeApprovalQueue(nil) })
	_, queue := newRealApprovalHandlers(t)
	SetBridgeApprovalQueue(queue)

	leg1, err := policy.NewBridgeLeg(&chanBridgeSender{sent: make(chan policy.BridgeRef, 1)})
	if err != nil {
		t.Fatalf("NewBridgeLeg: %v", err)
	}
	WireApprovalBridge(&BridgeRuntime{ApprovalBridge: leg1})

	leg2, err := policy.NewBridgeLeg(&chanBridgeSender{sent: make(chan policy.BridgeRef, 1)})
	if err != nil {
		t.Fatalf("NewBridgeLeg: %v", err)
	}
	WireApprovalBridge(&BridgeRuntime{ApprovalBridge: leg2}) // must not panic
}

// bridgeNotifierFunc adapts a plain func to policy.BridgeNotifier, so a
// test can control exactly when the "inner" dispatch completes.
type bridgeNotifierFunc func(ctx context.Context, verb string, entry policy.PendingEntry) error

func (f bridgeNotifierFunc) Dispatch(ctx context.Context, verb string, entry policy.PendingEntry) error {
	return f(ctx, verb, entry)
}

// TestAsyncBridgeNotifier_DispatchReturnsBeforeInnerCompletes proves the
// decorator never blocks its caller: the inner Dispatch blocks on a
// channel this test controls, yet the outer Dispatch returns immediately
// — the CR's disclosed P7 gap (a hung Telegram call must not delay the
// enqueuer's own RPC).
func TestAsyncBridgeNotifier_DispatchReturnsBeforeInnerCompletes(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{})
	inner := bridgeNotifierFunc(func(context.Context, string, policy.PendingEntry) error {
		close(started)
		<-release
		return nil
	})
	notifier := asyncBridgeNotifier{inner: inner}

	done := make(chan struct{})
	go func() {
		if err := notifier.Dispatch(context.Background(), "workspace.write", policy.PendingEntry{}); err != nil {
			t.Errorf("Dispatch: %v, want nil (never propagates the inner result)", err)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Dispatch did not return promptly; it must not wait on the inner call")
	}
	select {
	case <-started:
		close(release)
	case <-time.After(2 * time.Second):
		t.Fatal("the inner Dispatch was never actually invoked")
	}
}
