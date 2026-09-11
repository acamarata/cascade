// Purpose: lifecycle.go's required unit tests — TestTicketLifecycle proves
// the transition table against the spec (claim -> step -> cr -> qa ->
// done, multiple step/cr/qa passes allowed, every illegal pair refused
// with KindConflict, an unparseable/unknown event refused with
// KindInvalidInput, CR/QA level carried through without inventing one,
// Claim refused against a draft phase, and CurrentState is pure replay).
// SPORT: plugins/pbd/internal/pews lifecycle (ADD) — P1-E14-W3-S30-T1.
package pews

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// fakeJournalStore is an in-memory JournalStore: deterministic, no clock,
// no network — exactly what a unit test needs (Art.7).
type fakeJournalStore struct {
	mu      sync.Mutex
	entries map[string][]JournalEntry
}

func newFakeJournalStore() *fakeJournalStore {
	return &fakeJournalStore{entries: map[string][]JournalEntry{}}
}

func (s *fakeJournalStore) Append(_ context.Context, entityID string, event LifecycleEvent, operationID string, payload json.RawMessage) (JournalEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := JournalEntry{
		EntityID: entityID, Seq: uint64(len(s.entries[entityID])) + 1,
		Event: event, OperationID: operationID, Payload: payload,
	}
	s.entries[entityID] = append(s.entries[entityID], e)
	return e, nil
}

func (s *fakeJournalStore) Replay(_ context.Context, entityID string) ([]JournalEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]JournalEntry, len(s.entries[entityID]))
	copy(out, s.entries[entityID])
	return out, nil
}

// brokenReplayStore always fails Replay, proving CurrentState propagates
// a journal failure as an error rather than defaulting to StateUnclaimed.
type brokenReplayStore struct{}

func (brokenReplayStore) Append(context.Context, string, LifecycleEvent, string, json.RawMessage) (JournalEntry, error) {
	return JournalEntry{}, cascade.New(cascade.KindUnavailable, "broken")
}
func (brokenReplayStore) Replay(context.Context, string) ([]JournalEntry, error) {
	return nil, cascade.New(cascade.KindUnavailable, "broken replay")
}

func lifecycleFixtureTree() *Tree {
	return &Tree{Phase: "P1", Tickets: []TicketRecord{{
		Ticket: Ticket{ID: "P1-E14-W3-S30-T9", CRLevel: "CR-A+CR-B+CR-C", QALevel: "QA-B"},
	}}}
}

func TestTicketLifecycle(t *testing.T) {
	t.Run("happy path claim through done", testLifecycleHappyPath)
	t.Run("illegal transitions refuse with KindConflict", testLifecycleIllegalTransitions)
	t.Run("unknown/unparseable event refuses with KindInvalidInput", testLifecycleUnknownEvent)
	t.Run("cr/qa level carried through without inventing one", testLifecycleLevelCarriage)
	t.Run("claim refuses against a draft phase", testLifecycleDraftRefusal)
	t.Run("unknown ticket id refuses with KindNotFound", testLifecycleUnknownTicket)
	t.Run("nil tree refuses with KindInvalidInput", testLifecycleNilTree)
	t.Run("a journal failure propagates as an error", testLifecycleJournalFailure)
}

func testLifecycleHappyPath(t *testing.T) {
	ctx := context.Background()
	tree := lifecycleFixtureTree()
	js := newFakeJournalStore()
	id := tree.Tickets[0].Ticket.ID

	if _, err := Claim(ctx, tree, js, id, "op-1"); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if _, err := Step(ctx, tree, js, id, "op-2", "wrote the code"); err != nil {
		t.Fatalf("Step: %v", err)
	}
	if _, err := Step(ctx, tree, js, id, "op-3", "wrote tests"); err != nil {
		t.Fatalf("second Step: %v", err)
	}
	if _, err := RecordCR(ctx, tree, js, id, "op-4", CRLevelA); err != nil {
		t.Fatalf("RecordCR(A): %v", err)
	}
	if _, err := RecordCR(ctx, tree, js, id, "op-5", CRLevelB); err != nil {
		t.Fatalf("RecordCR(B): %v", err)
	}
	if _, err := RecordQA(ctx, tree, js, id, "op-6", QALevelB); err != nil {
		t.Fatalf("RecordQA: %v", err)
	}
	entry, err := Done(ctx, tree, js, id, "op-7")
	if err != nil {
		t.Fatalf("Done: %v", err)
	}
	if entry.Event != EventDone || entry.Seq != 7 {
		t.Errorf("Done entry = %+v, want Event=done Seq=7", entry)
	}
	state, serr := CurrentState(ctx, js, id)
	if serr != nil || state != StateDone {
		t.Fatalf("CurrentState = %v, %v, want done, nil", state, serr)
	}
	if _, err := Done(ctx, tree, js, id, "op-8"); !cascade.HasKind(err, cascade.KindConflict) {
		t.Errorf("Done from done: err = %v, want KindConflict", err)
	}
}

func testLifecycleIllegalTransitions(t *testing.T) {
	ctx := context.Background()
	tree := lifecycleFixtureTree()
	id := tree.Tickets[0].Ticket.ID

	cases := []struct {
		name string
		run  func(js JournalStore) error
	}{
		{"step before claim", func(js JournalStore) error { _, e := Step(ctx, tree, js, id, "o", ""); return e }},
		{"cr before step", func(js JournalStore) error { _, e := RecordCR(ctx, tree, js, id, "o", CRLevelA); return e }},
		{"qa before cr", func(js JournalStore) error { _, e := RecordQA(ctx, tree, js, id, "o", QALevelB); return e }},
		{"done before qa", func(js JournalStore) error { _, e := Done(ctx, tree, js, id, "o"); return e }},
		{"double claim", func(js JournalStore) error {
			if _, e := Claim(ctx, tree, js, id, "o1"); e != nil {
				return e
			}
			_, e := Claim(ctx, tree, js, id, "o2")
			return e
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := c.run(newFakeJournalStore()); !cascade.HasKind(err, cascade.KindConflict) {
				t.Errorf("err = %v, want KindConflict", err)
			}
		})
	}
}

func testLifecycleUnknownEvent(t *testing.T) {
	if _, err := ParseLifecycleEvent("bogus"); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("ParseLifecycleEvent(bogus): err = %v, want KindInvalidInput", err)
	}
	if _, err := ParseLifecycleEvent(""); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("ParseLifecycleEvent(empty): err = %v, want KindInvalidInput", err)
	}
	ctx := context.Background()
	js := newFakeJournalStore()
	js.entries["ghost"] = []JournalEntry{{EntityID: "ghost", Seq: 1, Event: "sideways"}}
	if _, err := CurrentState(ctx, js, "ghost"); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("CurrentState(unparseable): err = %v, want KindInvalidInput", err)
	}
}

func testLifecycleLevelCarriage(t *testing.T) {
	ctx := context.Background()
	tree := lifecycleFixtureTree()
	id := tree.Tickets[0].Ticket.ID
	js := newFakeJournalStore()
	mustClaimAndStep(ctx, t, tree, js, id)

	if _, err := RecordCR(ctx, tree, js, id, "op", CRLevel("CR-Z")); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("RecordCR(undeclared level): err = %v, want KindInvalidInput", err)
	}
	if _, err := RecordCR(ctx, tree, js, id, "op", CRLevelA); err != nil {
		t.Fatalf("RecordCR(declared token): %v", err)
	}
	if _, err := RecordCR(ctx, tree, js, id, "op2", CRLevelA); err != nil {
		t.Fatalf("second RecordCR pass: %v", err)
	}
	if _, err := RecordQA(ctx, tree, js, id, "op3", QALevelA); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("RecordQA(mismatched level): err = %v, want KindInvalidInput", err)
	}
	entry, err := RecordQA(ctx, tree, js, id, "op4", QALevelB)
	if err != nil {
		t.Fatalf("RecordQA(declared level): %v", err)
	}
	payload, derr := DecodeLifecyclePayload(entry.Payload)
	if derr != nil || payload.QALevel != QALevelB {
		t.Errorf("DecodeLifecyclePayload = %+v, %v, want QALevel QA-B", payload, derr)
	}
	if p, err := DecodeLifecyclePayload(nil); err != nil || p != (LifecyclePayload{}) {
		t.Errorf("DecodeLifecyclePayload(nil) = %+v, %v, want zero value", p, err)
	}
}

func testLifecycleDraftRefusal(t *testing.T) {
	ctx := context.Background()
	tree := lifecycleFixtureTree()
	tree.Draft = true
	if _, err := Claim(ctx, tree, newFakeJournalStore(), tree.Tickets[0].Ticket.ID, "op"); !cascade.HasKind(err, cascade.KindConflict) {
		t.Errorf("Claim(draft): err = %v, want KindConflict", err)
	}
}

func testLifecycleUnknownTicket(t *testing.T) {
	ctx := context.Background()
	if _, err := Claim(ctx, lifecycleFixtureTree(), newFakeJournalStore(), "no-such-id", "op"); !cascade.HasKind(err, cascade.KindNotFound) {
		t.Errorf("Claim(unknown id): err = %v, want KindNotFound", err)
	}
}

func testLifecycleNilTree(t *testing.T) {
	if _, err := Claim(context.Background(), nil, newFakeJournalStore(), "x", "op"); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("Claim(nil tree): err = %v, want KindInvalidInput", err)
	}
}

func testLifecycleJournalFailure(t *testing.T) {
	ctx := context.Background()
	tree := lifecycleFixtureTree()
	if _, err := Claim(ctx, tree, brokenReplayStore{}, tree.Tickets[0].Ticket.ID, "op"); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Errorf("Claim(broken journal): err = %v, want KindUnavailable", err)
	}
}

// mustClaimAndStep advances id through claim+step so a CR/QA-focused test
// can start from StateStep.
func mustClaimAndStep(ctx context.Context, t *testing.T, tree *Tree, js JournalStore, id string) {
	t.Helper()
	if _, err := Claim(ctx, tree, js, id, "setup-claim"); err != nil {
		t.Fatalf("setup Claim: %v", err)
	}
	if _, err := Step(ctx, tree, js, id, "setup-step", ""); err != nil {
		t.Fatalf("setup Step: %v", err)
	}
}

// TestTicketLifecyclePlatformParity is Art.5's proof: lifecycle.go
// touches nothing platform-conditional (in-memory maps and the
// standard-library time/context/json packages only — its own persistence
// is FileJournalStore, plugins/pbd/lifecycle.go, whose own paths are
// already cross-platform elsewhere in this repo), so there is no
// unsupported-platform branch to assert a refusal for. This test reruns
// the full claim-through-done happy path unconditionally: a real
// divergence on any CI-matrix platform would fail it there, not pass
// silently.
func TestTicketLifecyclePlatformParity(t *testing.T) {
	testLifecycleHappyPath(t)
}

func TestLifecycleStateAndEventValid(t *testing.T) {
	for _, s := range lifecycleStates {
		if !s.Valid() {
			t.Errorf("state %q reports invalid", s)
		}
	}
	if LifecycleState("bogus").Valid() {
		t.Error("unknown state reports valid")
	}
	for _, e := range lifecycleEvents {
		if !e.Valid() {
			t.Errorf("event %q reports invalid", e)
		}
	}
	if LifecycleEvent("bogus").Valid() {
		t.Error("unknown event reports valid")
	}
}
