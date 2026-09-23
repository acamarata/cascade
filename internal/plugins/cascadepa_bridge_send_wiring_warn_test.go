// Purpose (this file): the two WireApprovalBridge silent-return paths the
//
//	S-48.T4 producer confirming review flagged (FLAG-B) — no approval queue
//	ever injected, and an injected queue whose concrete type does not
//	implement bridgeArmableQueue. Split from cascadepa_bridge_send_wiring_
//	test.go, which already sits at Art.10.3's 300-line cap, so these two
//	Warn-logging proofs get their own file rather than pushing it over.
//
// Constraints: no global state survives a test — every SetBridgeApprovalQueue
//
//	call is paired with a t.Cleanup(nil) restore, and every slog.SetDefault
//	override is paired with a t.Cleanup restore of the prior default,
//	mirroring review_wiring_test.go's own established pattern for testing
//	the same slog.Default() seam.
//
// SPORT: internal/plugins:cascadepa-bridge-send-wiring/TEST (CHG) —
//
//	P1-E23-W5-S48-T4 producer confirming review, FLAG-B.
package plugins

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/policy"
)

// noSetBridgeQueue satisfies policy.ApprovalQueue but not the narrower
// bridgeArmableQueue interface WireApprovalBridge's own type assertion
// checks for — proving the assertion's failure path really triggers on a
// queue of the wrong concrete type, not only on queue == nil.
type noSetBridgeQueue struct{}

func (noSetBridgeQueue) Enqueue(context.Context, policy.EnqueueRequest) (policy.EnqueueResult, error) {
	return policy.EnqueueResult{}, nil
}

func (noSetBridgeQueue) GetPending(context.Context) ([]policy.PendingEntry, error) { return nil, nil }

func (noSetBridgeQueue) Decide(context.Context, []policy.DecisionRequest) ([]policy.DecisionOutcome, error) {
	return nil, nil
}

func (noSetBridgeQueue) Cancel(context.Context, string) error { return nil }

func (noSetBridgeQueue) ConsumeToken(context.Context, policy.ConsumeRequest) (policy.ConsumeResult, error) {
	return policy.ConsumeResult{}, nil
}

func (noSetBridgeQueue) Expire(context.Context) (int, error) { return 0, nil }

// TestWireApprovalBridge_NoInjectedQueue_LogsWarn proves the queue == nil
// silent-return path is now observable: no queue was ever injected, so
// WireApprovalBridge must log a Warn naming the bridge's subject rather
// than leaving an operator with nothing but "notifications never arrive"
// to diagnose a genuine startup mis-order against.
func TestWireApprovalBridge_NoInjectedQueue_LogsWarn(t *testing.T) {
	SetBridgeApprovalQueue(nil)
	var buf bytes.Buffer
	restore := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(restore) })

	leg, err := policy.NewBridgeLeg(&chanBridgeSender{sent: make(chan policy.BridgeRef, 1)})
	if err != nil {
		t.Fatalf("NewBridgeLeg: %v", err)
	}
	WireApprovalBridge(&BridgeRuntime{Subject: "tg-warn-nilqueue", ApprovalBridge: leg})

	line := buf.String()
	if !strings.Contains(line, "no approval queue was injected") || !strings.Contains(line, "tg-warn-nilqueue") {
		t.Errorf("WireApprovalBridge with no injected queue logged %q, want a Warn naming the subject", line)
	}
}

// TestWireApprovalBridge_QueueWithoutSetBridge_LogsWarn proves the second
// silent-return path: an injected queue whose concrete type does not
// implement bridgeArmableQueue now logs a Warn naming that type, instead of
// arming nothing with no trace anywhere.
func TestWireApprovalBridge_QueueWithoutSetBridge_LogsWarn(t *testing.T) {
	t.Cleanup(func() { SetBridgeApprovalQueue(nil) })
	SetBridgeApprovalQueue(noSetBridgeQueue{})
	var buf bytes.Buffer
	restore := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(restore) })

	leg, err := policy.NewBridgeLeg(&chanBridgeSender{sent: make(chan policy.BridgeRef, 1)})
	if err != nil {
		t.Fatalf("NewBridgeLeg: %v", err)
	}
	WireApprovalBridge(&BridgeRuntime{Subject: "tg-warn-badtype", ApprovalBridge: leg})

	line := buf.String()
	if !strings.Contains(line, "does not implement SetBridge") || !strings.Contains(line, "noSetBridgeQueue") {
		t.Errorf("WireApprovalBridge with a non-bridgeArmableQueue logged %q, want a Warn naming the type", line)
	}
}
