// Purpose: the memory.review.* RPC surface under test — that both methods
//
//	are registered on a real router (a handler nothing mounts is a
//	subsystem that ships unreachable), that params decode into concrete
//	structs, and that a malformed or hostile params blob is refused as a
//	taxonomy error rather than acted on.
//
// SPORT: internal/memory/review (ADD, P1-E07-W2-S14-T3).
package review

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/acamarata/cascade/internal/memory"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestRegisterMountsBothMethods(t *testing.T) {
	f := newFixture(t)
	registry := rpc.NewRegistry()
	NewHandler(f.queue).Register(registry)

	for _, method := range []string{MethodReviewList, MethodReviewAct} {
		if !registry.Registered(method) {
			t.Errorf("%s is not registered on the router", method)
		}
	}
}

func TestListMethodServesTheQueueAndWritesNothing(t *testing.T) {
	f := newFixture(t)
	f.observe(t, memory.KindProject, "below", "s-1")
	before := treeDigest(t, f.base)
	h := NewHandler(f.queue)

	raw, err := h.List(context.Background(), json.RawMessage(`{"section":"pending"}`))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	got, ok := raw.(ListResult)
	if !ok {
		t.Fatalf("result is %T, want ListResult", raw)
	}
	if len(got.Pending) != 1 || got.Pending[0].ID != "project/below" {
		t.Errorf("pending = %+v", got.Pending)
	}
	if after := treeDigest(t, f.base); after != before {
		t.Fatalf("serving a list changed the store: %s -> %s", before, after)
	}
}

func TestListMethodWithNoParamsListsEverything(t *testing.T) {
	f := newFixture(t)
	f.observe(t, memory.KindProject, "below", "s-1")
	h := NewHandler(f.queue)

	for _, params := range []json.RawMessage{nil, json.RawMessage("null"), json.RawMessage("  ")} {
		raw, err := h.List(context.Background(), params)
		if err != nil {
			t.Fatalf("List(%q): %v", params, err)
		}
		if len(raw.(ListResult).Pending) != 1 {
			t.Errorf("List(%q) returned %+v", params, raw)
		}
	}
}

func TestActMethodCarriesOutTheNamedAction(t *testing.T) {
	f := newFixture(t)
	f.observe(t, memory.KindProject, "below", "s-1")
	h := NewHandler(f.queue)

	raw, err := h.Act(context.Background(),
		json.RawMessage(`{"id":"project/below","action":"approve"}`))
	if err != nil {
		t.Fatalf("Act: %v", err)
	}
	got, ok := raw.(ActResult)
	if !ok {
		t.Fatalf("result is %T, want ActResult", raw)
	}
	if got.Item.Status != memory.CandidatePromoted {
		t.Errorf("status = %q, want promoted", got.Item.Status)
	}
}

func TestMalformedParamsAreRefusedNotActedOn(t *testing.T) {
	f := newFixture(t)
	f.observe(t, memory.KindProject, "below", "s-1")
	before := treeDigest(t, f.base)
	h := NewHandler(f.queue)

	cases := map[string]json.RawMessage{
		"truncated":             json.RawMessage(`{"id":"project/below"`),
		"wrong type on action":  json.RawMessage(`{"id":"project/below","action":42}`),
		"wrong type on window":  json.RawMessage(`{"id":"project/below","action":"defer","defer_days":"7"}`),
		"an action nobody owns": json.RawMessage(`{"id":"project/below","action":"delete"}`),
	}
	for name, params := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := h.Act(context.Background(), params); err == nil {
				t.Fatal("accepted, want a refusal")
			} else if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
				t.Errorf("kind = %v (ok=%v), want invalid_input", kind, ok)
			}
		})
	}
	if after := treeDigest(t, f.base); after != before {
		t.Fatalf("a refused call changed the store: %s -> %s", before, after)
	}
}
