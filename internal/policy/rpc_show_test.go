// Purpose: approval.show reports every retained entry's state through the
//
//	entry source, with exactly the bridge-safe keys, and never turns a read
//	error into "not found".
//
// SPORT: internal/policy approval.show state tests (P1-SEC-41).

package policy

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// showKeys is approval.show's JSON key set for id, and its state.
func showKeys(t *testing.T, deps RPCDeps, id string) ([]string, map[string]any) {
	t.Helper()
	out, err := MethodHandlers(deps)["approval.show"](context.Background(), json.RawMessage(`{"request_id":"`+id+`"}`))
	if err != nil {
		t.Fatalf("approval.show %s: %v", id, err)
	}
	raw, _ := json.Marshal(out)
	fields := map[string]any{}
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys, fields
}

// seedEveryState retains one entry in each state but expired (the clock
// is not moved): pending, approved, consumed, consuming (set under q.mu),
// denied and canceled.
func (g *redeemFixture) seedEveryState(t *testing.T) map[string]string {
	t.Helper()
	ctx := context.Background()
	ids := map[string]string{"pending": g.enqueue(t, "pending").RequestID, "approved": g.approved(t, "approved").RequestID,
		"consuming": g.enqueue(t, "consuming").RequestID, "denied": g.enqueue(t, "denied").RequestID,
		"canceled": g.enqueue(t, "canceled").RequestID}
	consumed := g.approved(t, "edit")
	if _, err := g.redeem(consumed, "edit"); err != nil {
		t.Fatal(err)
	}
	ids["consumed"] = consumed.RequestID
	g.queue.mu.Lock()
	g.queue.entries[ids["consuming"]].state = ApprovalConsuming
	g.queue.mu.Unlock()
	if _, err := g.queue.Decide(ctx, []DecisionRequest{{RequestID: ids["denied"]}}); err != nil {
		t.Fatal(err)
	}
	if err := g.queue.Cancel(ctx, ids["canceled"]); err != nil {
		t.Fatal(err)
	}
	return ids
}

func TestApprovalShowReportsState(t *testing.T) {
	ctx := context.Background()
	g := newRedeemFixture(t)
	ids := g.seedEveryState(t)
	lapse := newRedeemFixture(t)
	expired := lapse.enqueue(t, "expired")
	lapse.clock.Advance(MaxApprovalTTL + time.Second)
	want := "action_class,action_summary,exp,request_id,state"
	check := func(deps RPCDeps, state, id string) map[string]any {
		keys, fields := showKeys(t, deps, id)
		if strings.Join(keys, ",") != want || fields["state"] != state || fields["request_id"] != id {
			t.Fatalf("%s: approval.show = %v, want keys %s and state %s", state, fields, want, state)
		}
		return fields
	}
	for state, id := range ids {
		check(g.deps, state, id)
	}
	check(lapse.deps, "expired", expired.RequestID)
	pending, err := g.queue.GetPending(ctx)
	if err != nil || len(pending) != 1 {
		t.Fatalf("GetPending = %v, %v", pending, err)
	}
	if fields := check(g.deps, "pending", ids["pending"]); fields["action_class"] != float64(pending[0].ActionClass) || pending[0].ActionClass == 0 {
		t.Fatalf("pending action_class = %v, GetPending's = %v", fields["action_class"], pending[0].ActionClass)
	}
	if _, err := MethodHandlers(g.deps)["approval.show"](ctx, json.RawMessage(`{"request_id":"no-such-request"}`)); !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("unknown id = %v, want KindNotFound", err)
	}
	readErr := cascade.New(cascade.KindUnavailable, "fake entry read failure")
	deps := g.deps
	deps.Entries = failingEntries{src: g.queue, err: readErr}
	_, err = MethodHandlers(deps)["approval.show"](ctx, json.RawMessage(`{"request_id":"`+ids["approved"]+`"}`))
	if !inChain(err, readErr) || cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("show with a failing entry source = %v, want that error, never KindNotFound", err)
	}
}

// TestApprovalShowRefusalLegs covers the reads that never reach a queue
// entry: a process with neither an entry source nor a queue refuses as
// unavailable, a nil queue's EntryView refuses as invalid input, and an
// entry read with no capability registry reports the invalid zero class
// rather than guessing one.
func TestApprovalShowRefusalLegs(t *testing.T) {
	ctx := context.Background()
	params := json.RawMessage(`{"request_id":"any-request"}`)
	if _, err := MethodHandlers(RPCDeps{})["approval.show"](ctx, params); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("approval.show with no queue = %v, want KindUnavailable", err)
	}
	var nilQueue *StoreApprovals
	if _, err := nilQueue.EntryView(ctx, "any-request"); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("EntryView on a nil queue = %v, want KindInvalidInput", err)
	}
	g := newRedeemFixture(t)
	approved := g.approved(t, "approved")
	deps := g.deps
	deps.Registry = nil
	_, fields := showKeys(t, deps, approved.RequestID)
	if fields["action_class"] != float64(0) || fields["state"] != "approved" {
		t.Fatalf("approval.show without a registry = %v, want action_class 0 and state approved", fields)
	}
}
