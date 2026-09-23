// Purpose: unit coverage for bridge_leg.go's fail-closed gate (class AND
//
//	verb) and its exactly-once projection to BridgeSender.Send.
//
// SPORT: internal/policy bridge_leg/TEST (P1-E23-W5-S48-T4, T0 D4).
package policy

import (
	"context"
	"errors"
	"testing"
)

// recordingBridgeSender captures every Send call, so a test can assert
// "sender never called" as well as "called exactly once with this ref".
type recordingBridgeSender struct {
	calls []BridgeRef
	err   error
}

func (s *recordingBridgeSender) Send(_ context.Context, ref BridgeRef) error {
	s.calls = append(s.calls, ref)
	return s.err
}

func TestNewBridgeLeg_NilSenderRefused(t *testing.T) {
	if _, err := NewBridgeLeg(nil); err == nil {
		t.Fatal("NewBridgeLeg(nil): err = nil, want a refusal")
	}
}

// TestBridgeLeg_NonBridgeableClass_SenderNeverCalled proves a real,
// registered-but-not-allow-listed class (ClassRead, ClassExternalSideEffect,
// ClassDestructivePrivileged) never reaches Send and mints nothing.
//
// FLAG-3: the error identity check is pointer equality, not errors.Is —
// errors.Is on a *cascade.Error only compares Kind (pkg/cascade/errors.go),
// so it would still pass for ANY KindPermissionDenied error, not just
// ErrNotBridgeable specifically. See MUTATION M-A in bridge_leg_test's
// sibling report: returning a different KindPermissionDenied error from
// Dispatch must fail this assertion.
func TestBridgeLeg_NonBridgeableClass_SenderNeverCalled(t *testing.T) {
	for _, class := range []ActionClass{ClassRead, ClassExternalSideEffect, ClassDestructivePrivileged} {
		t.Run(class.String(), func(t *testing.T) {
			sender := &recordingBridgeSender{}
			leg, err := NewBridgeLeg(sender)
			if err != nil {
				t.Fatalf("NewBridgeLeg: %v", err)
			}
			entry := PendingEntry{RequestID: "req-non-bridgeable", ActionClass: class}
			gotErr := leg.Dispatch(context.Background(), "workspace.write", entry)
			if gotErr != ErrNotBridgeable {
				t.Errorf("Dispatch err = %v, want ErrNotBridgeable (identity)", gotErr)
			}
			if len(sender.calls) != 0 {
				t.Errorf("Send called %d times, want 0", len(sender.calls))
			}
		})
	}
}

// TestBridgeLeg_UnknownClass_Refused proves the invalid zero class and an
// out-of-range value both refuse identically to a real non-bridgeable
// class — fail-closed on "unresolvable", not merely on "known-bad".
func TestBridgeLeg_UnknownClass_Refused(t *testing.T) {
	for _, class := range []ActionClass{ActionClass(0), ActionClass(200)} {
		sender := &recordingBridgeSender{}
		leg, err := NewBridgeLeg(sender)
		if err != nil {
			t.Fatalf("NewBridgeLeg: %v", err)
		}
		entry := PendingEntry{RequestID: "req-unknown-class", ActionClass: class}
		if gotErr := leg.Dispatch(context.Background(), "workspace.write", entry); gotErr != ErrNotBridgeable {
			t.Errorf("class %d: Dispatch err = %v, want ErrNotBridgeable (identity)", class, gotErr)
		}
		if len(sender.calls) != 0 {
			t.Errorf("class %d: Send called %d times, want 0", class, len(sender.calls))
		}
	}
}

// TestBridgeLeg_ElevationClassVerb_SenderNeverCalled proves FLAG-1: a
// §5.14 elevation-class verb (approval_matrix.go's elevationClassVerbs,
// e.g. "policy.set") is refused even though its class is on the
// bridgeable allow-list — the class test alone cannot see that a verb is
// local-only, so Dispatch must gate on CanBridgeVerb(verb, class), not
// CanBridge(class) alone.
func TestBridgeLeg_ElevationClassVerb_SenderNeverCalled(t *testing.T) {
	sender := &recordingBridgeSender{}
	leg, err := NewBridgeLeg(sender)
	if err != nil {
		t.Fatalf("NewBridgeLeg: %v", err)
	}
	entry := PendingEntry{RequestID: "req-elevated", ActionClass: ClassWorkspaceMutation}
	gotErr := leg.Dispatch(context.Background(), "policy.set", entry)
	if gotErr != ErrNotBridgeable {
		t.Errorf("Dispatch err = %v, want ErrNotBridgeable for an elevation-class verb", gotErr)
	}
	if len(sender.calls) != 0 {
		t.Errorf("Send called %d times for an elevation-class verb, want 0", len(sender.calls))
	}

	// The same class, a non-elevated verb: must still send.
	if err := leg.Dispatch(context.Background(), "workspace.write", entry); err != nil {
		t.Fatalf("Dispatch(workspace.write): %v", err)
	}
	if len(sender.calls) != 1 {
		t.Errorf("Send called %d times for a non-elevated verb, want exactly 1", len(sender.calls))
	}
}

// TestBridgeLeg_Bridgeable_SendsExactlyOnceWithRequestIDOnly proves the
// happy path: exactly one Send, carrying nothing but the request id — no
// action hash, no params, no summary, no expiry (BridgeRef has one field,
// so this also proves the projection never grows a second one silently).
func TestBridgeLeg_Bridgeable_SendsExactlyOnceWithRequestIDOnly(t *testing.T) {
	for _, class := range []ActionClass{ClassLocalDev, ClassWorkspaceMutation} {
		t.Run(class.String(), func(t *testing.T) {
			sender := &recordingBridgeSender{}
			leg, err := NewBridgeLeg(sender)
			if err != nil {
				t.Fatalf("NewBridgeLeg: %v", err)
			}
			entry := PendingEntry{
				RequestID: "01J8ZC5W2K4F6H8M0P2R4T6V8X", Summary: "write x",
				ActionClass: class,
			}
			if err := leg.Dispatch(context.Background(), "workspace.write", entry); err != nil {
				t.Fatalf("Dispatch: %v", err)
			}
			if len(sender.calls) != 1 {
				t.Fatalf("Send called %d times, want exactly 1", len(sender.calls))
			}
			got := sender.calls[0]
			if got.RequestID.String() != entry.RequestID {
				t.Errorf("ref.RequestID = %q, want %q", got.RequestID.String(), entry.RequestID)
			}
		})
	}
}

func TestBridgeLeg_SendFailure_Propagates(t *testing.T) {
	wantErr := errors.New("boom: telegram unreachable")
	sender := &recordingBridgeSender{err: wantErr}
	leg, err := NewBridgeLeg(sender)
	if err != nil {
		t.Fatalf("NewBridgeLeg: %v", err)
	}
	entry := PendingEntry{RequestID: "req-transport-fail", ActionClass: ClassWorkspaceMutation}
	gotErr := leg.Dispatch(context.Background(), "workspace.write", entry)
	if !errors.Is(gotErr, wantErr) {
		t.Errorf("Dispatch err = %v, want it to wrap %v", gotErr, wantErr)
	}
	if len(sender.calls) != 1 {
		t.Errorf("Send called %d times, want exactly 1", len(sender.calls))
	}
}

func TestBridgeLeg_NilLeg_Refuses(t *testing.T) {
	var leg *BridgeLeg
	err := leg.Dispatch(context.Background(), "workspace.write", PendingEntry{ActionClass: ClassWorkspaceMutation})
	if err == nil {
		t.Fatal("Dispatch on a nil leg: err = nil, want a refusal")
	}
}

// TestStoreApprovals_Enqueue_NotifiesBridgeOnFreshAdmission is the
// end-to-end proof of the "pending-entry event": a real Enqueue, over a
// real SQLite-backed queue (newApprovalFixtureWith, approval_queue_test.go),
// notifies a configured Bridge exactly once on a fresh admission, and NOT
// again on a coalesced (deduplicated) re-admission of the same action.
func TestStoreApprovals_Enqueue_NotifiesBridgeOnFreshAdmission(t *testing.T) {
	sender := &recordingBridgeSender{}
	leg, err := NewBridgeLeg(sender)
	if err != nil {
		t.Fatalf("NewBridgeLeg: %v", err)
	}
	f := newApprovalFixtureWith(t, ApprovalQueueConfig{Bridge: leg})

	res := f.enqueue(t, "notify-me")
	if len(sender.calls) != 1 {
		t.Fatalf("Send called %d times, want exactly 1 on a fresh admission", len(sender.calls))
	}
	if sender.calls[0].RequestID.String() != res.RequestID {
		t.Errorf("notified request id = %q, want %q", sender.calls[0].RequestID.String(), res.RequestID)
	}

	dup := f.enqueue(t, "notify-me")
	if !dup.Deduplicated {
		t.Fatal("second Enqueue of the same action: Deduplicated = false, want true")
	}
	if len(sender.calls) != 1 {
		t.Errorf("Send called %d times after a deduplicated admission, want still 1", len(sender.calls))
	}
}

// TestStoreApprovals_Enqueue_ElevationClassVerbNeverNotified is the
// end-to-end proof of FLAG-1: an admitted entry whose CAPABILITY NAME is
// itself a §5.14 elevation-class verb ("policy.set") must never reach the
// bridge sender, even though its class (ClassWorkspaceMutation) is on the
// bridgeable allow-list. Enqueue's own admission-time local-only check
// (approval_queue_enqueue.go's localOnly) only inspects req.Verb, which
// this request leaves empty — so this proves notifyBridge's OWN gate
// (entry.capability threaded into CanBridgeVerb), not the admission gate.
func TestStoreApprovals_Enqueue_ElevationClassVerbNeverNotified(t *testing.T) {
	sender := &recordingBridgeSender{}
	leg, err := NewBridgeLeg(sender)
	if err != nil {
		t.Fatalf("NewBridgeLeg: %v", err)
	}
	f := newApprovalFixtureWith(t, ApprovalQueueConfig{Bridge: leg})
	if err := f.reg.Add(context.Background(), Capability{
		Name: "policy.set", Desc: "elevation-class verb probe (FLAG-1)", DefaultPolicy: ClassWorkspaceMutation,
	}); err != nil {
		t.Fatalf("registering policy.set: %v", err)
	}

	elevated := askRequest("elevate-me")
	elevated.Capability = "policy.set"
	if _, err := f.queue.Enqueue(context.Background(), elevated); err != nil {
		t.Fatalf("Enqueue(policy.set): %v", err)
	}
	if len(sender.calls) != 0 {
		t.Errorf("Send called %d times for an elevation-class verb, want 0", len(sender.calls))
	}

	f.enqueue(t, "still-bridgeable")
	if len(sender.calls) != 1 {
		t.Errorf("Send called %d times for workspace.write, want exactly 1", len(sender.calls))
	}
}

// TestStoreApprovals_Enqueue_BridgeFailureDoesNotFailAdmission proves a
// refusing/failing sender never fails Enqueue itself — the entry is still
// validly queued whether or not a remote surface heard about it.
func TestStoreApprovals_Enqueue_BridgeFailureDoesNotFailAdmission(t *testing.T) {
	sender := &recordingBridgeSender{err: errors.New("boom: telegram unreachable")}
	leg, err := NewBridgeLeg(sender)
	if err != nil {
		t.Fatalf("NewBridgeLeg: %v", err)
	}
	f := newApprovalFixtureWith(t, ApprovalQueueConfig{Bridge: leg})
	if _, err := f.queue.Enqueue(context.Background(), askRequest("still-queues")); err != nil {
		t.Fatalf("Enqueue: %v, want it to succeed despite the bridge send failing", err)
	}
	if len(sender.calls) != 1 {
		t.Errorf("Send called %d times, want exactly 1", len(sender.calls))
	}
}

// TestStoreApprovals_SetBridge_NilRefused proves SetBridge cannot be used
// to leave the queue looking wired while arming nothing.
func TestStoreApprovals_SetBridge_NilRefused(t *testing.T) {
	f := newApprovalFixtureWith(t, ApprovalQueueConfig{})
	if err := f.queue.SetBridge(nil); err == nil {
		t.Fatal("SetBridge(nil): err = nil, want a refusal")
	}
}

// TestStoreApprovals_SetBridge_SecondCallRefused proves the queue does not
// silently pick a winner between two bridges.
func TestStoreApprovals_SetBridge_SecondCallRefused(t *testing.T) {
	f := newApprovalFixtureWith(t, ApprovalQueueConfig{})
	leg1, err := NewBridgeLeg(&recordingBridgeSender{})
	if err != nil {
		t.Fatalf("NewBridgeLeg: %v", err)
	}
	if err := f.queue.SetBridge(leg1); err != nil {
		t.Fatalf("first SetBridge: %v", err)
	}
	leg2, err := NewBridgeLeg(&recordingBridgeSender{})
	if err != nil {
		t.Fatalf("NewBridgeLeg: %v", err)
	}
	if err := f.queue.SetBridge(leg2); err == nil {
		t.Fatal("second SetBridge: err = nil, want a refusal")
	}
}

// TestStoreApprovals_SetBridge_ThenEnqueue_NotifiesExactlyOnce is the
// FIX-0 end-to-end proof: a queue built with NO bridge (as the
// composition root must, since the sender's collaborators are not ready
// at NewApprovalQueue time), wired later via SetBridge, notifies a fresh
// admission exactly once.
func TestStoreApprovals_SetBridge_ThenEnqueue_NotifiesExactlyOnce(t *testing.T) {
	f := newApprovalFixtureWith(t, ApprovalQueueConfig{})
	sender := &recordingBridgeSender{}
	leg, err := NewBridgeLeg(sender)
	if err != nil {
		t.Fatalf("NewBridgeLeg: %v", err)
	}
	if err := f.queue.SetBridge(leg); err != nil {
		t.Fatalf("SetBridge: %v", err)
	}
	f.enqueue(t, "wired-late")
	if len(sender.calls) != 1 {
		t.Errorf("Send called %d times after SetBridge, want exactly 1", len(sender.calls))
	}
}
