package governor

// Purpose: compile-time proof that each of the five seam interfaces is
// satisfiable by a plain stub, plus the shared test fixtures
// escalation_test.go's Advance suite is built on: fakeJournalStore (an
// in-memory journal.Store), fixedConfidence, countingSeams (Retryer +
// ContextEnricher + SupervisorCreator + HumanNotifier in one, with a
// per-rung call counter and configurable failure), and the small helpers
// that seed or decode an EscalationEvent. Living here, rather than in
// escalation_test.go, keeps that file under the 300-line cap.

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/internal/runtime"
)

// seamStub satisfies all five seam interfaces at once with a single
// configurable error, purely to prove each interface's method set is
// implementable by an ordinary struct with no hidden requirements.
type seamStub struct{ err error }

func (s seamStub) Confidence(_ context.Context, _ string) (float64, error) {
	return 1, s.err
}
func (s seamStub) Retry(_ context.Context, _ string) error { return s.err }
func (s seamStub) Enrich(_ context.Context, _ string) (int, error) {
	return 0, s.err
}
func (s seamStub) CreateSupervisor(_ context.Context, _ string) (string, error) {
	return "", s.err
}
func (s seamStub) Notify(_ context.Context, _ EscalationEvent) error { return s.err }

var (
	_ ConfidenceProvider = seamStub{}
	_ Retryer            = seamStub{}
	_ ContextEnricher    = seamStub{}
	_ SupervisorCreator  = seamStub{}
	_ HumanNotifier      = seamStub{}
)

func TestEscalationSeamStubPropagatesConfiguredError(t *testing.T) {
	want := errors.New("seam boom")
	s := seamStub{err: want}
	ctx := context.Background()

	if _, err := s.Confidence(ctx, "e"); !errors.Is(err, want) {
		t.Errorf("Confidence err = %v, want %v", err, want)
	}
	if err := s.Retry(ctx, "e"); !errors.Is(err, want) {
		t.Errorf("Retry err = %v, want %v", err, want)
	}
	if _, err := s.Enrich(ctx, "e"); !errors.Is(err, want) {
		t.Errorf("Enrich err = %v, want %v", err, want)
	}
	if _, err := s.CreateSupervisor(ctx, "e"); !errors.Is(err, want) {
		t.Errorf("CreateSupervisor err = %v, want %v", err, want)
	}
	if err := s.Notify(ctx, EscalationEvent{}); !errors.Is(err, want) {
		t.Errorf("Notify err = %v, want %v", err, want)
	}
}

func TestEscalationSeamStubSucceedsWithNilError(t *testing.T) {
	s := seamStub{}
	ctx := context.Background()

	if _, err := s.Confidence(ctx, "e"); err != nil {
		t.Errorf("Confidence err = %v, want nil", err)
	}
	if err := s.Retry(ctx, "e"); err != nil {
		t.Errorf("Retry err = %v, want nil", err)
	}
}

// fakeJournalStore is an in-memory journal.Store: no I/O, no clock of its
// own (Append stamps TSUnixNano as 0; nothing under test reads it), and
// optional one-shot failure injection for Append/Replay.
type fakeJournalStore struct {
	mu         sync.Mutex
	entries    map[string][]journal.Entry
	seq        map[string]uint64
	appendFail error
	replayFail error
}

func newFakeJournalStore() *fakeJournalStore {
	return &fakeJournalStore{entries: make(map[string][]journal.Entry), seq: make(map[string]uint64)}
}

func (f *fakeJournalStore) Append(_ context.Context, entityID string, kind journal.Kind, opID string, payload json.RawMessage) (journal.Entry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.appendFail != nil {
		return journal.Entry{}, f.appendFail
	}
	f.seq[entityID]++
	e := journal.Entry{EntityID: entityID, Seq: f.seq[entityID], Kind: kind, OperationID: opID, Payload: payload}
	f.entries[entityID] = append(f.entries[entityID], e)
	return e, nil
}

func (f *fakeJournalStore) Checkpoint(_ context.Context, _ journal.Cursor) error { return nil }

func (f *fakeJournalStore) Replay(_ context.Context, entityID string, _ journal.Cursor, kinds []journal.Kind) ([]journal.Entry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.replayFail != nil {
		return nil, f.replayFail
	}
	allowed := make(map[journal.Kind]bool, len(kinds))
	for _, k := range kinds {
		allowed[k] = true
	}
	var out []journal.Entry
	for _, e := range f.entries[entityID] {
		if len(kinds) == 0 || allowed[e.Kind] {
			out = append(out, e)
		}
	}
	return out, nil
}

func (f *fakeJournalStore) Close() error { return nil }

func (f *fakeJournalStore) count(entityID string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.entries[entityID])
}

// seedEvent appends an EscalationEvent directly, bypassing the ladder, to
// set up a starting rung/attempt precondition.
func seedEvent(t *testing.T, store *fakeJournalStore, entityID string, rung EscalationRung, attempt int) {
	t.Helper()
	payload, err := json.Marshal(EscalationEvent{EntityID: entityID, Rung: rung, Attempt: attempt})
	if err != nil {
		t.Fatalf("seedEvent marshal: %v", err)
	}
	if _, err := store.Append(context.Background(), entityID, journal.KindEscalation, "seed-"+rung.String(), payload); err != nil {
		t.Fatalf("seedEvent append: %v", err)
	}
}

// decodeLast returns entityID's most recently journaled EscalationEvent.
func decodeLast(t *testing.T, store *fakeJournalStore, entityID string) EscalationEvent {
	t.Helper()
	entries, err := store.Replay(context.Background(), entityID, journal.Cursor{}, []journal.Kind{journal.KindEscalation})
	if err != nil || len(entries) == 0 {
		t.Fatalf("Replay: %v (len=%d)", err, len(entries))
	}
	var e EscalationEvent
	if err := json.Unmarshal(entries[len(entries)-1].Payload, &e); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	return e
}

// fixedConfidence is a ConfidenceProvider returning a fixed value or error.
type fixedConfidence struct {
	v   float64
	err error
}

func (c fixedConfidence) Confidence(context.Context, string) (float64, error) { return c.v, c.err }

// countingSeams satisfies Retryer, ContextEnricher, SupervisorCreator and
// HumanNotifier at once, counting calls per rung and returning a
// per-rung-configurable error.
type countingSeams struct {
	mu    sync.Mutex
	calls map[EscalationRung]int
	fail  map[EscalationRung]error
}

func newCountingSeams() *countingSeams {
	return &countingSeams{calls: make(map[EscalationRung]int), fail: make(map[EscalationRung]error)}
}

func (s *countingSeams) record(rung EscalationRung) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls[rung]++
	return s.fail[rung]
}

func (s *countingSeams) callCount(rung EscalationRung) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[rung]
}

func (s *countingSeams) Retry(context.Context, string) error { return s.record(RungRetry) }
func (s *countingSeams) Enrich(context.Context, string) (int, error) {
	return 0, s.record(RungContext)
}
func (s *countingSeams) CreateSupervisor(context.Context, string) (string, error) {
	return "", s.record(RungSupervisorTask)
}
func (s *countingSeams) Notify(context.Context, EscalationEvent) error {
	return s.record(RungHuman)
}

func testPolicy() EscalationPolicy {
	return EscalationPolicy{
		MaxAttempts: map[EscalationRung]int{
			RungRetry: 100, RungContext: 100, RungSupervisorTask: 100, RungHuman: 100,
		},
		ConfidenceThreshold: 0.5,
	}
}

func newTestEscalationLadder(store *fakeJournalStore, conf ConfidenceProvider, seams *countingSeams, policy EscalationPolicy, clk runtime.Clock) *EscalationLadder {
	return NewEscalationLadder(store, conf, seams, seams, seams, seams, policy, clk)
}
