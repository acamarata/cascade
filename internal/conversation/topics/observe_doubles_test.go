// Package topics (observe_doubles_test.go): Purpose: the helpers and store
// doubles ObserveLogger's four test files share - wantEq, mustObserveLogger,
// the standard two-turn window and its segmenter, the published-event
// decoder, the first_use_at seeder, and two provider.Store doubles
// (onceStore, racedStore) that make the window's failure and race paths
// reachable without a real backend. Split out of observe_log_test.go only to
// keep every file under the 300-line cap (Art.10.3), the same reason
// reassign_doubles_test.go exists.
package topics

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// wantEq is a one-line comparable-value assertion this ticket's test files
// share.
func wantEq[T comparable](t *testing.T, got, want T, msg string) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: got %v, want %v", msg, got, want)
	}
}

// mustObserveLogger wires a ready ObserveLogger over the four dependencies
// or fails the test immediately.
func mustObserveLogger(
	t *testing.T, at *AutoThreader, store provider.Store, clock Clock, pub AuditPublisher,
) *ObserveLogger {
	t.Helper()
	ol, err := NewObserveLogger(at, store, clock, pub)
	if err != nil {
		t.Fatalf("NewObserveLogger: %v", err)
	}
	return ol
}

// observeTurns is the two-turn window these tests route. With
// codeBoundarySegmenter's boundary at turn 1 it plans two segments: the
// implicit turn-zero segment (no label, testTaxonomy resolves it to
// "fallback") and the "code" segment ("code-topic").
func observeTurns() []Turn {
	return []Turn{{Speaker: "a", Text: "general question"}, {Speaker: "a", Text: "some code"}}
}

func codeBoundarySegmenter() *fakeSegmenter {
	return &fakeSegmenter{boundaries: []Boundary{{TurnIndex: 1, Label: "code"}}}
}

// decodeObserveEvent decodes one published payload into observeLogEvent.
func decodeObserveEvent(t *testing.T, payload []byte) observeLogEvent {
	t.Helper()
	var evt observeLogEvent
	if err := json.Unmarshal(payload, &evt); err != nil {
		t.Fatalf("decoding published event: %v", err)
	}
	return evt
}

// seedFirstUseAt writes at directly under the state row, bypassing
// ObserveLogger, so a fresh instance's FIRST Observe sees a chosen elapsed.
func seedFirstUseAt(t *testing.T, store provider.Store, at time.Time) {
	t.Helper()
	raw, err := json.Marshal(observeStateRecord{TopicType: observeStateScope, FirstUseAt: at.Format(time.RFC3339)})
	if err != nil {
		t.Fatalf("seedFirstUseAt: encoding: %v", err)
	}
	if err := store.Put(context.Background(), observeStateNamespace, observeStateKey, raw); err != nil {
		t.Fatalf("seedFirstUseAt: Put: %v", err)
	}
}

// onceStore wraps a real Store, refusing Get/Put/Tx past its call limit -
// which is how a test proves the in-process cache served a second Observe
// rather than reading the row again.
type onceStore struct {
	provider.Store
	calls int
	limit int
}

func (s *onceStore) refuse() error {
	s.calls++
	if s.calls > s.limit {
		return cascade.New(cascade.KindUnavailable, "onceStore: refused past its call limit")
	}
	return nil
}

func (s *onceStore) Get(ctx context.Context, namespace, key string) ([]byte, error) {
	if err := s.refuse(); err != nil {
		return nil, err
	}
	return s.Store.Get(ctx, namespace, key)
}

func (s *onceStore) Put(ctx context.Context, namespace, key string, value []byte) error {
	if err := s.refuse(); err != nil {
		return err
	}
	return s.Store.Put(ctx, namespace, key, value)
}

func (s *onceStore) Tx(ctx context.Context, fn func(context.Context, provider.Tx) error) error {
	if err := s.refuse(); err != nil {
		return err
	}
	return s.Store.Tx(ctx, fn)
}

// racedStore lies exactly once: its FIRST Get reports "no row yet" while the
// row a racing instance already committed really is there, so
// initFirstUseAt's conditional create hits the KindConflict its
// never-overwritten guarantee rests on. Every later Get tells the truth,
// which is what the conflict path then reads.
type racedStore struct {
	provider.Store
	gets int
}

func (s *racedStore) Get(ctx context.Context, namespace, key string) ([]byte, error) {
	s.gets++
	if s.gets == 1 {
		return nil, cascade.New(cascade.KindNotFound, "racedStore: the first read saw no row")
	}
	return s.Store.Get(ctx, namespace, key)
}
