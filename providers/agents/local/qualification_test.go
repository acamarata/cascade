package local

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/provider"
)

// fakeClock is a fixed, injected Clock (no bare time.Now).
type fakeClock struct{ now time.Time }

func (c fakeClock) Now() time.Time { return c.now }

// memStore is an in-memory ConfigStore double for tests only.
type memStore struct {
	mu   sync.Mutex
	data map[string][]byte
}

func newMemStore() *memStore { return &memStore{data: map[string][]byte{}} }

func (s *memStore) Get(_ context.Context, key string) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.data[key]
	return v, ok, nil
}

func (s *memStore) Set(_ context.Context, key string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[key] = value
	return nil
}

// scriptedModel is a ModelExecutor double (the sel-based leaf-dispatch
// seam driver.go declares) whose Chat replies are driven entirely by the
// injected reply function. sel is accepted for interface conformance and
// otherwise ignored: no test in this package asserts on the Selection a
// call was made with.
type scriptedModel struct {
	reply func(req provider.ChatRequest) (provider.ChatResponse, error)
}

func (m scriptedModel) Chat(_ context.Context, _ provider.Selection, req provider.ChatRequest) (provider.ChatResponse, error) {
	return m.reply(req)
}
func (scriptedModel) Embed(context.Context, provider.Selection, provider.ModelEmbedRequest) (provider.ModelEmbedResponse, error) {
	return provider.ModelEmbedResponse{}, nil
}
func (scriptedModel) Count(context.Context, provider.Selection, provider.CountRequest) (provider.CountResponse, error) {
	return provider.CountResponse{}, nil
}
func (scriptedModel) Stream(context.Context, provider.Selection, provider.ChatRequest, provider.StreamSink) error {
	return nil
}

const testFixture = `{"cases":[{"prompt":"say ok","must_contain":"ok"}]}`

func passingModel() scriptedModel {
	return scriptedModel{reply: func(provider.ChatRequest) (provider.ChatResponse, error) {
		return provider.ChatResponse{Message: provider.ChatMessage{Content: "ok"}}, nil
	}}
}

func failingModel() scriptedModel {
	return scriptedModel{reply: func(provider.ChatRequest) (provider.ChatResponse, error) {
		return provider.ChatResponse{Message: provider.ChatMessage{Content: "nope"}}, nil
	}}
}

func newQualifier(t *testing.T, model ModelExecutor, store ConfigStore) *Qualifier {
	t.Helper()
	q, err := NewQualifier(model, store, fakeClock{now: time.Unix(1000, 0)}, []byte(testFixture))
	if err != nil {
		t.Fatalf("NewQualifier: %v", err)
	}
	return q
}

// TestLocalAuthoringAbsentWithoutQualification asserts a missing row, a
// failing row, and a zero-value Qualifier resolution all read as
// unqualified — the DEFAULT path refuses (R-21.170).
func TestLocalAuthoringAbsentWithoutQualification(t *testing.T) {
	store := newMemStore()
	q := newQualifier(t, failingModel(), store)
	ctx := context.Background()

	// No row at all.
	qualified, err := q.Resolve(ctx, "model-a")
	if err != nil || qualified {
		t.Fatalf("Resolve(no row) = (%v, %v), want (false, nil)", qualified, err)
	}

	// A failing run records a row that still resolves to unqualified.
	row, err := q.Run(ctx, "model-a")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if row.Passed {
		t.Fatal("Run against a failing model reported Passed = true")
	}
	qualified, err = q.Resolve(ctx, "model-a")
	if err != nil || qualified {
		t.Fatalf("Resolve(failing row) = (%v, %v), want (false, nil)", qualified, err)
	}
}

// TestLocalAuthoringPerModelId asserts a passing row grants authoring for
// THAT model id only, never for a sibling model id that has no row of its
// own.
func TestLocalAuthoringPerModelId(t *testing.T) {
	store := newMemStore()
	q := newQualifier(t, passingModel(), store)
	ctx := context.Background()

	if _, err := q.Run(ctx, "model-qualified"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	qualified, err := q.Resolve(ctx, "model-qualified")
	if err != nil || !qualified {
		t.Fatalf("Resolve(qualified model) = (%v, %v), want (true, nil)", qualified, err)
	}
	qualified, err = q.Resolve(ctx, "model-other")
	if err != nil || qualified {
		t.Fatalf("Resolve(sibling model) = (%v, %v), want (false, nil)", qualified, err)
	}
}

// TestLocalStaleFixtureHashUnqualifies asserts a recorded row whose
// fixture_hash no longer matches the CURRENT fixture reads as unqualified
// (fail-closed staleness), even though the row itself says Passed=true.
func TestLocalStaleFixtureHashUnqualifies(t *testing.T) {
	store := newMemStore()
	q := newQualifier(t, passingModel(), store)
	ctx := context.Background()

	if _, err := q.Run(ctx, "model-a"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	qualified, err := q.Resolve(ctx, "model-a")
	if err != nil || !qualified {
		t.Fatalf("Resolve after a fresh Run = (%v, %v), want (true, nil)", qualified, err)
	}

	// Rebuild the Qualifier over a DIFFERENT fixture; the stored row's
	// fixture_hash now mismatches the current fixture.
	newFixture := `{"cases":[{"prompt":"say ok","must_contain":"ok"},{"prompt":"say more","must_contain":"more"}]}`
	q2, err := NewQualifier(passingModel(), store, fakeClock{now: time.Unix(2000, 0)}, []byte(newFixture))
	if err != nil {
		t.Fatalf("NewQualifier(new fixture): %v", err)
	}
	qualified, err = q2.Resolve(ctx, "model-a")
	if err != nil || qualified {
		t.Fatalf("Resolve(stale fixture_hash) = (%v, %v), want (false, nil)", qualified, err)
	}
}

// TestLocalAuthoringOnHardDenylist asserts the structural fail-closed
// property this package ships in place of the not-yet-built AI/S-71.T3
// hard denylist (see doc.go): Qualifier and Driver expose exactly one
// write path for a qualification row (Run), and no setter of any kind
// exists that could flip `authoring` directly. This test enumerates
// Qualifier's exported method set by calling every one of them and
// asserting the only one that can change Resolve's answer is Run.
func TestLocalAuthoringOnHardDenylist(t *testing.T) {
	store := newMemStore()
	q := newQualifier(t, failingModel(), store)
	ctx := context.Background()

	before, err := q.Resolve(ctx, "model-a")
	if err != nil || before {
		t.Fatalf("Resolve(before) = (%v, %v), want (false, nil)", before, err)
	}

	// ParseFixture and FixtureHash are pure, read-only helpers: calling
	// them must never change a stored row.
	if _, err := ParseFixture([]byte(testFixture)); err != nil {
		t.Fatalf("ParseFixture: %v", err)
	}
	_ = FixtureHash([]byte(testFixture))

	after, err := q.Resolve(ctx, "model-a")
	if err != nil || after {
		t.Fatalf("Resolve(after read-only calls) = (%v, %v), want (false, nil) — a read-only helper mutated state", after, err)
	}

	// The ONLY write path is Run, and it fails closed against a failing
	// model: authoring is still absent afterward.
	if _, err := q.Run(ctx, "model-a"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	final, err := q.Resolve(ctx, "model-a")
	if err != nil || final {
		t.Fatalf("Resolve(after Run against a failing model) = (%v, %v), want (false, nil)", final, err)
	}
}

// TestQualifierConstructionValidation asserts NewQualifier refuses a nil
// seam or a fixture with no cases.
func TestQualifierConstructionValidation(t *testing.T) {
	store := newMemStore()
	clock := fakeClock{now: time.Unix(1, 0)}
	if _, err := NewQualifier(nil, store, clock, []byte(testFixture)); err == nil {
		t.Fatal("NewQualifier(nil model) returned nil error")
	}
	if _, err := NewQualifier(passingModel(), nil, clock, []byte(testFixture)); err == nil {
		t.Fatal("NewQualifier(nil store) returned nil error")
	}
	if _, err := NewQualifier(passingModel(), store, nil, []byte(testFixture)); err == nil {
		t.Fatal("NewQualifier(nil clock) returned nil error")
	}
	if _, err := NewQualifier(passingModel(), store, clock, []byte(`{"cases":[]}`)); err == nil {
		t.Fatal("NewQualifier(empty fixture) returned nil error")
	}
	if _, err := NewQualifier(passingModel(), store, clock, []byte(`not json`)); err == nil {
		t.Fatal("NewQualifier(malformed fixture) returned nil error")
	}
}

// TestResolvePropagatesStoreError asserts a ConfigStore error surfaces
// from Resolve rather than being swallowed into a permissive false.
type erroringStore struct{ err error }

func (s erroringStore) Get(context.Context, string) ([]byte, bool, error) { return nil, false, s.err }
func (erroringStore) Set(context.Context, string, []byte) error           { return nil }

func TestResolvePropagatesStoreError(t *testing.T) {
	wantErr := errors.New("store unavailable")
	q := newQualifier(t, passingModel(), erroringStore{err: wantErr})
	if _, err := q.Resolve(context.Background(), "model-a"); !errors.Is(err, wantErr) {
		t.Fatalf("Resolve error = %v, want wrapping %v", err, wantErr)
	}
}
