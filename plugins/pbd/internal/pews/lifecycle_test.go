// Purpose: lifecycle.go's required unit tests — the transition table against
// the spec, illegal-transition refusals, level carriage, draft refusal, and
// (P1-PBD-07) the CASAppender seam's KindUnsupported refusal. SPORT: plugins/pbd/internal/pews lifecycle (ADD) — P1-E14-W3-S30-T1.
package pews

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// fakeJournalStore is an in-memory JournalStore (Art.7): deterministic, no clock, no network.
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

// AppendIf implements CASAppender in-memory, mu-serialized (single process only; real atomicity is FileJournalStore's).
func (s *fakeJournalStore) AppendIf(_ context.Context, entityID string, expectedSeq int, event LifecycleEvent, operationID string, payload json.RawMessage) (JournalEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	existing := s.entries[entityID]
	for _, e := range existing {
		if e.OperationID != operationID {
			continue
		}
		if e.Event != event {
			return JournalEntry{}, cascade.Newf(cascade.KindConflict, "fakeJournalStore: operation_id %q already recorded event %q, not %q", operationID, e.Event, event)
		}
		return e, nil
	}
	if len(existing) != expectedSeq {
		return JournalEntry{}, cascade.Newf(cascade.KindConflict, "fakeJournalStore: entity %q: expected seq %d, have %d", entityID, expectedSeq, len(existing))
	}
	e := JournalEntry{EntityID: entityID, Seq: uint64(len(existing)) + 1, Event: event, OperationID: operationID, Payload: payload}
	s.entries[entityID] = append(existing, e)
	return e, nil
}

var _ CASAppender = (*fakeJournalStore)(nil)

// brokenReplayStore always fails Replay (CurrentState must propagate it).
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
	t.Run("edge cases: draft/unknown-ticket/nil-tree/broken-journal", testLifecycleEdgeCases)
}

func testLifecycleHappyPath(t *testing.T) {
	ctx := context.Background()
	tree := lifecycleFixtureTree()
	js := newFakeJournalStore()
	id := tree.Tickets[0].Ticket.ID

	steps := []struct {
		name string
		run  func() (JournalEntry, error)
	}{
		{"Claim", func() (JournalEntry, error) { return Claim(ctx, tree, js, id, "op-1") }},
		{"Step", func() (JournalEntry, error) { return Step(ctx, tree, js, id, "op-2", "wrote the code") }},
		{"second Step", func() (JournalEntry, error) { return Step(ctx, tree, js, id, "op-3", "wrote tests") }},
		{"RecordCR(A)", func() (JournalEntry, error) { return RecordCR(ctx, tree, js, id, "op-4", CRLevelA) }},
		{"RecordCR(B)", func() (JournalEntry, error) { return RecordCR(ctx, tree, js, id, "op-5", CRLevelB) }},
		{"RecordQA", func() (JournalEntry, error) { return RecordQA(ctx, tree, js, id, "op-6", QALevelB) }},
	}
	for _, s := range steps {
		if _, err := s.run(); err != nil {
			t.Fatalf("%s: %v", s.name, err)
		}
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
	for _, s := range []string{"bogus", ""} {
		if _, err := ParseLifecycleEvent(s); !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Errorf("ParseLifecycleEvent(%q): err = %v, want KindInvalidInput", s, err)
		}
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

// testLifecycleEdgeCases covers draft-phase refusal, an unknown ticket id, a
// nil tree, and a broken journal — four distinct Claim refusals, one per row, each asserted by Kind alone (a table, not four near-identical funcs).
func testLifecycleEdgeCases(t *testing.T) {
	ctx := context.Background()
	fixture := lifecycleFixtureTree()
	draft := lifecycleFixtureTree()
	draft.Draft = true
	cases := []struct {
		name string
		tree *Tree
		js   JournalStore
		id   string
		want cascade.Kind
	}{
		{"draft phase", draft, newFakeJournalStore(), draft.Tickets[0].Ticket.ID, cascade.KindConflict},
		{"unknown ticket id", fixture, newFakeJournalStore(), "no-such-id", cascade.KindNotFound},
		{"nil tree", nil, newFakeJournalStore(), "x", cascade.KindInvalidInput},
		{"broken journal", fixture, brokenReplayStore{}, fixture.Tickets[0].Ticket.ID, cascade.KindUnavailable},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := Claim(ctx, c.tree, c.js, c.id, "op"); !cascade.HasKind(err, c.want) {
				t.Errorf("Claim: err = %v, want %v", err, c.want)
			}
		})
	}
}

// mustClaimAndStep advances id through claim+step (CR/QA tests start here).
func mustClaimAndStep(ctx context.Context, t *testing.T, tree *Tree, js JournalStore, id string) {
	t.Helper()
	if _, err := Claim(ctx, tree, js, id, "setup-claim"); err != nil {
		t.Fatalf("setup Claim: %v", err)
	}
	if _, err := Step(ctx, tree, js, id, "setup-step", ""); err != nil {
		t.Fatalf("setup Step: %v", err)
	}
}

// TestTicketLifecyclePlatformParity is Art.5's proof: lifecycle.go touches
// nothing platform-conditional, so this reruns the happy path
// unconditionally rather than asserting an unsupported-platform refusal.
func TestTicketLifecyclePlatformParity(t *testing.T) {
	testLifecycleHappyPath(t)
}

func TestLifecycleStateAndEventValid(t *testing.T) {
	for _, s := range lifecycleStates {
		if !s.Valid() {
			t.Errorf("state %q reports invalid", s)
		}
	}
	for _, e := range lifecycleEvents {
		if !e.Valid() {
			t.Errorf("event %q reports invalid", e)
		}
	}
	if LifecycleState("bogus").Valid() || LifecycleEvent("bogus").Valid() {
		t.Error("unknown state/event reports valid")
	}
}

// nonCASJournalStore forwards Append/Replay only — never AppendIf — so it does NOT implement CASAppender (proves Claim's KindUnsupported refusal).
type nonCASJournalStore struct{ inner *fakeJournalStore }

func (s nonCASJournalStore) Append(ctx context.Context, id string, e LifecycleEvent, op string, p json.RawMessage) (JournalEntry, error) {
	return s.inner.Append(ctx, id, e, op, p)
}
func (s nonCASJournalStore) Replay(ctx context.Context, id string) ([]JournalEntry, error) {
	return s.inner.Replay(ctx, id)
}

// TestClaimRefusesStoreWithoutCAS is acceptance[4].
func TestClaimRefusesStoreWithoutCAS(t *testing.T) {
	ctx := context.Background()
	tree := lifecycleFixtureTree()
	id := tree.Tickets[0].Ticket.ID
	js := nonCASJournalStore{inner: newFakeJournalStore()}

	_, claimErr := Claim(ctx, tree, js, id, "op-claim")
	wantMsg := fmt.Sprintf("pews: claim requires a journal store implementing CASAppender, got %T", js)
	if cerr, ok := claimErr.(*cascade.Error); !ok || cerr.Kind != cascade.KindUnsupported || cerr.Msg != wantMsg {
		t.Fatalf("Claim(no CASAppender): err = %v, want Kind=KindUnsupported Msg=%q", claimErr, wantMsg)
	}
	if entries, _ := js.Replay(ctx, id); len(entries) != 0 {
		t.Fatalf("Replay = %+v, want empty (refused before any append)", entries)
	}
	if _, err := js.Append(ctx, id, EventClaim, "seed-claim", nil); err != nil {
		t.Fatalf("seeding claim entry: %v", err)
	}
	if _, err := Step(ctx, tree, js, id, "op-step", ""); err != nil {
		t.Errorf("Step(no CASAppender store): err = %v, want nil", err)
	}
}
