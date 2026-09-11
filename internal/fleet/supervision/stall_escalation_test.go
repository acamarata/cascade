package supervision

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/fleet/governor"
)

// fakeApprovalRequester records every RequestApproval call, for
// asserting the human-escalation seam without a real internal/policy
// queue (see stall_escalation.go's CONTRADICTION on why this ticket
// declares its own narrower seam).
type fakeApprovalRequester struct {
	calls  []StallEvent
	err    error
	nextID int
}

func (f *fakeApprovalRequester) RequestApproval(_ context.Context, _ string, event StallEvent) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	f.calls = append(f.calls, event)
	f.nextID++
	return fmt.Sprintf("req-%d", f.nextID), nil
}

func TestStallSupervisorPushesKindStallItem(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, time.Now())
	sup := &stallSupervisor{store: store, lookup: func(string) (StallEvent, bool) { return StallEvent{}, false }}

	id, err := sup.CreateSupervisor(ctx, "sess-1")
	if err != nil {
		t.Fatalf("CreateSupervisor: %v", err)
	}
	if id == "" {
		t.Fatal("CreateSupervisor returned an empty id")
	}
	item, err := store.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if item.Kind != KindStall {
		t.Errorf("item.Kind = %s, want KindStall", item.Kind)
	}
	if item.SourceRef != "sess-1" {
		t.Errorf("item.SourceRef = %s, want sess-1", item.SourceRef)
	}
}

// TestStallSupervisorIdempotentOnRepeat proves the supervisor-task seam
// reuses Store.Push's own (Kind, SourceRef) dedup rather than creating a
// second queue entry for a session that already has one — the ticket's
// "second invocation on an already-escalated session is a no-op"
// requirement, discharged by the collaborator it reuses.
func TestStallSupervisorIdempotentOnRepeat(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, time.Now())
	sup := &stallSupervisor{store: store, lookup: func(string) (StallEvent, bool) { return StallEvent{}, false }}

	first, err := sup.CreateSupervisor(ctx, "sess-1")
	if err != nil {
		t.Fatalf("1st CreateSupervisor: %v", err)
	}
	second, err := sup.CreateSupervisor(ctx, "sess-1")
	if err != nil {
		t.Fatalf("2nd CreateSupervisor: %v", err)
	}
	if first != second {
		t.Errorf("2nd CreateSupervisor id = %s, want the same id as the 1st (%s)", second, first)
	}
	items, err := store.ListInScopes(ctx, []ScopeRef{sessionScope("sess-1")}, Filter{})
	if err != nil {
		t.Fatalf("ListInScopes: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want exactly 1 (no duplicate push)", len(items))
	}
}

func TestStallNotifierUsesLookupEvent(t *testing.T) {
	want := StallEvent{SessionID: "sess-1", StallKind: StallKindGateDenied, StalledSince: 42}
	req := &fakeApprovalRequester{}
	n := &stallNotifier{requester: req, lookup: func(string) (StallEvent, bool) { return want, true }}

	err := n.Notify(context.Background(), governor.EscalationEvent{EntityID: "sess-1", Rung: governor.RungHuman})
	if err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if len(req.calls) != 1 || req.calls[0] != want {
		t.Errorf("requester.calls = %v, want [%v]", req.calls, want)
	}
}

// TestStallNotifierFallsBackToUnknownWhenNoLookupHit proves a miss never
// fabricates detail: it reports StallKindUnknown rather than a zero
// value that would read as a real classification.
func TestStallNotifierFallsBackToUnknownWhenNoLookupHit(t *testing.T) {
	req := &fakeApprovalRequester{}
	n := &stallNotifier{requester: req, lookup: func(string) (StallEvent, bool) { return StallEvent{}, false }}

	if err := n.Notify(context.Background(), governor.EscalationEvent{EntityID: "sess-2"}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if len(req.calls) != 1 || req.calls[0].StallKind != StallKindUnknown {
		t.Errorf("requester.calls = %v, want one call with StallKindUnknown", req.calls)
	}
}

func TestStallNotifierPropagatesRequesterError(t *testing.T) {
	sentinel := governor.ErrEscalationInvalidInput
	req := &fakeApprovalRequester{err: sentinel}
	n := &stallNotifier{requester: req, lookup: func(string) (StallEvent, bool) { return StallEvent{}, false }}

	if err := n.Notify(context.Background(), governor.EscalationEvent{EntityID: "sess-3"}); err != sentinel {
		t.Errorf("Notify err = %v, want %v", err, sentinel)
	}
}
