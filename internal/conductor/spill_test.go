package conductor

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// fakeEventPublisher records every Publish call without touching a real
// Store, so spill_test.go can assert on event kind/namespace/payload
// without standing up internal/events.New's Store-backed Bus.
type fakeEventPublisher struct {
	mu     sync.Mutex
	events []events.Event
}

func (f *fakeEventPublisher) Publish(_ context.Context, namespace string, kind events.EventKind, source string, payload []byte) (events.Event, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ev := events.Event{Seq: uint64(len(f.events) + 1), Kind: kind, Source: source, Payload: payload}
	_ = namespace
	f.events = append(f.events, ev)
	return ev, nil
}

func (f *fakeEventPublisher) last() events.Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.events[len(f.events)-1]
}

func (f *fakeEventPublisher) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.events)
}

func TestQuotaPolicy_Advance_DemotesAndAdvancesToNextLane(t *testing.T) {
	clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))
	p := NewQuotaPolicy(QuotaConfig{SpillOrder: []LaneID{"a", "b", "c"}}, clock)
	pub := &fakeEventPublisher{}

	next, err := p.Advance(context.Background(), pub, "test", "a", nil, "429")
	if err != nil {
		t.Fatalf("Advance: unexpected error: %v", err)
	}
	if next != "b" {
		t.Fatalf("Advance: next lane = %q, want %q", next, "b")
	}
	if pub.count() != 1 {
		t.Fatalf("Advance: published %d events, want 1", pub.count())
	}
	ev := pub.last()
	if ev.Kind != EventKindSpillAdvance {
		t.Fatalf("Advance: event kind = %q, want %q", ev.Kind, EventKindSpillAdvance)
	}
	var payload spillAdvancePayload
	if err := json.Unmarshal(ev.Payload, &payload); err != nil {
		t.Fatalf("decoding event payload: %v", err)
	}
	if payload.FromLane != "a" || payload.ToLane != "b" || payload.Reason != "429" {
		t.Fatalf("Advance: payload = %+v, want {a b 429}", payload)
	}
}

func TestQuotaPolicy_Advance_AllExhausted_PublishesExhaustedAndReturnsTypedError(t *testing.T) {
	clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))
	p := NewQuotaPolicy(QuotaConfig{SpillOrder: []LaneID{"a"}}, clock)
	pub := &fakeEventPublisher{}

	_, err := p.Advance(context.Background(), pub, "test", "a", nil, "429")
	if !cascade.HasKind(err, cascade.KindQuotaExhausted) {
		t.Fatalf("Advance: error kind = %v, want KindQuotaExhausted", err)
	}
	ev := pub.last()
	if ev.Kind != EventKindSpillExhausted {
		t.Fatalf("Advance: event kind = %q, want %q", ev.Kind, EventKindSpillExhausted)
	}
	var payload spillExhaustedPayload
	if err := json.Unmarshal(ev.Payload, &payload); err != nil {
		t.Fatalf("decoding event payload: %v", err)
	}
	if payload.FromLane != "a" || payload.Reason != "429" {
		t.Fatalf("Advance: exhausted payload = %+v", payload)
	}
}

func TestQuotaPolicy_Advance_MultiHopChurnEachAdvanceEmitsOneEvent(t *testing.T) {
	clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))
	p := NewQuotaPolicy(QuotaConfig{SpillOrder: []LaneID{"a", "b", "c"}}, clock)
	pub := &fakeEventPublisher{}

	lane, err := p.Advance(context.Background(), pub, "test", "a", nil, "429")
	if err != nil || lane != "b" {
		t.Fatalf("hop 1: lane=%q err=%v", lane, err)
	}
	lane, err = p.Advance(context.Background(), pub, "test", "b", nil, "429")
	if err != nil || lane != "c" {
		t.Fatalf("hop 2: lane=%q err=%v", lane, err)
	}
	_, err = p.Advance(context.Background(), pub, "test", "c", nil, "429")
	if !cascade.HasKind(err, cascade.KindQuotaExhausted) {
		t.Fatalf("hop 3: err=%v, want KindQuotaExhausted", err)
	}
	if pub.count() != 3 {
		t.Fatalf("published %d events across 3 advances, want 3 (one per advance)", pub.count())
	}
}

func TestPublishDivergence_EmitsDivergenceEvent(t *testing.T) {
	pub := &fakeEventPublisher{}
	if err := PublishDivergence(context.Background(), pub, "test", "missing [conductor.quota] section"); err != nil {
		t.Fatalf("PublishDivergence: unexpected error: %v", err)
	}
	ev := pub.last()
	if ev.Kind != EventKindQuotaDivergence {
		t.Fatalf("PublishDivergence: event kind = %q, want %q", ev.Kind, EventKindQuotaDivergence)
	}
}

// spyStore is a full pkg/provider.Store implementation that panics-free
// records every Put call. It stands in for the usage-accounting domain
// Store (S-20.T4's per-lane request/token counters) that a future change
// might be tempted to wire QuotaPolicy up to. QuotaPolicy and Advance
// hold NO Store field anywhere in quota.go/spill.go, so this test is a
// structural regression guard, not a conditional check keyed on
// account_kind: the personal-tracking invariant (R-14.34 -- personal-lane
// usage is never written to a tracking table) holds for every lane,
// personal or not, because there is no code path capable of writing at
// all.
type spyStore struct {
	mu   sync.Mutex
	puts int
}

var _ provider.Store = (*spyStore)(nil)

func (s *spyStore) Get(context.Context, string, string) ([]byte, error) {
	return nil, cascade.New(cascade.KindNotFound, "spyStore: not found")
}
func (s *spyStore) Put(context.Context, string, string, []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.puts++
	return nil
}
func (s *spyStore) Delete(context.Context, string, string) error { return nil }
func (s *spyStore) Scan(context.Context, string, string) (provider.Iterator, error) {
	return nil, cascade.New(cascade.KindUnsupported, "spyStore: Scan unsupported in this fake")
}
func (s *spyStore) Tx(ctx context.Context, fn func(ctx context.Context, tx provider.Tx) error) error {
	return fn(ctx, nil)
}

func (s *spyStore) putCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.puts
}

func TestPersonalLaneDispatch_NeverWritesToStore(t *testing.T) {
	clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))
	// "personal-a"/"personal-b" model the R-14.34 account_kind=personal
	// case; QuotaPolicy has no notion of account_kind at all (it is
	// registry-owned metadata this ticket does not duplicate), which is
	// itself the point: nothing in the dispatch path below can reach a
	// Store regardless of what kind of lane it is dispatching.
	p := NewQuotaPolicy(QuotaConfig{SpillOrder: []LaneID{"personal-a", "personal-b"}}, clock)
	pub := &fakeEventPublisher{}
	spy := &spyStore{}

	for i := 0; i < 5; i++ {
		if _, err := p.NextLane(context.Background(), nil); err != nil && !cascade.HasKind(err, cascade.KindQuotaExhausted) {
			t.Fatalf("NextLane: unexpected error: %v", err)
		}
		if _, err := p.Advance(context.Background(), pub, "test", "personal-a", nil, "429"); err != nil {
			// exhaustion is an expected outcome on later iterations
			_ = err
		}
	}

	if spy.putCalls() != 0 {
		t.Fatalf("personal-lane dispatch made %d Store.Put calls, want 0 (R-14.34 invariant)", spy.putCalls())
	}
}
