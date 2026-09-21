package main

// Purpose: end-to-end proof of P1-E21-W5-S46-T4 D2's wiring -- a real
//   sqlite conversation.Store, a real *events.Bus, a real topics
//   composition (ThreadStore/TaxonomyConfig/ExemplarStore/AutoThreader/
//   ObserveLogger), driven entirely through chat.append_turn's real RPC
//   entry point (registry.Dispatch), with fakes ONLY at the true provider
//   seam (provider.Embedder, provider.ModelExecutor) -- exactly what a
//   production embedder/executor would satisfy once the disclosed gaps in
//   chat_topics_engine.go's resolvers close.
// SPORT: cmd/cascade chat topics engine (ADD tests) -- P1-E21-W5-S46-T4 D2.

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/conversation"
	"github.com/acamarata/cascade/internal/conversation/topics"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/provider"
)

// mutableClock is a settable Clock double satisfying every Now()-only
// Clock shape this test needs (conversation.Clock, topics.Clock,
// runtime.Clock, internal/events' clock) with zero adapter code.
type mutableClock struct{ now time.Time }

func (c *mutableClock) Now() time.Time { return c.now }

func newTestChatClock() *mutableClock {
	return &mutableClock{now: time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)}
}

// fakeSegmenterEmbedder is a deterministic, dependency-free
// provider.Embedder: two turns whose text shares the same "topicN"
// marker get identical vectors (cosine distance 0, no spike); different
// markers get orthogonal vectors (cosine distance 1, always a spike).
type fakeSegmenterEmbedder struct{}

func (fakeSegmenterEmbedder) Model() provider.EmbedModel {
	return provider.EmbedModel{ID: "fake-topics-embed-v1", Dimensions: 2}
}

func (fakeSegmenterEmbedder) Embed(_ context.Context, inputs []provider.EmbedInput) ([]provider.EmbedOutput, error) {
	out := make([]provider.EmbedOutput, len(inputs))
	for i, in := range inputs {
		vec := []float32{0, 1}
		if strings.Contains(in.Text, "topicA") {
			vec = []float32{1, 0}
		}
		out[i] = provider.EmbedOutput{Vector: vec, Model: provider.EmbedModel{ID: "fake-topics-embed-v1", Dimensions: 2}}
	}
	return out, nil
}

// fakeSegmenterExecutor is a deterministic provider.ModelExecutor: it
// classifies a turn by the same "topicN" marker its paired embedder keys
// on, so segmenter_core.go's spike map and label map agree by
// construction. failAfter, when > 0, makes the (failAfter)th call return
// an error -- the D6 "embedder error surfaces as topic_warning" case
// reuses this to fail deterministically on a specific turn.
type fakeSegmenterExecutor struct {
	calls     int
	failAfter int
}

func (f *fakeSegmenterExecutor) Execute(_ context.Context, req provider.ModelRequest) (provider.ModelResponse, error) {
	f.calls++
	if f.failAfter > 0 && f.calls >= f.failAfter {
		return provider.ModelResponse{}, errTestExecutorFailure
	}
	label := "topicB"
	if strings.Contains(req.Inputs[0].Content, "topicA") {
		label = "topicA"
	}
	return provider.ModelResponse{Output: label}, nil
}

var errTestExecutorFailure = &testExecutorError{}

type testExecutorError struct{}

func (*testExecutorError) Error() string { return "fake segmenter executor: forced failure" }

// chatTopicsFixture wires one full, real composition (store, bus,
// registry, topic engine) for this file's tests. fail requests an
// executor that fails on its 3rd classify call (used by the "error
// surfaces as warning" test); ok requests the always-succeeding double.
type chatTopicsFixture struct {
	t        *testing.T
	clock    *mutableClock
	store    conversation.Store
	bus      *events.Bus
	registry *rpc.Registry
	engine   *chatTopicEngine
}

func newChatTopicsFixture(t *testing.T, failAfter int) *chatTopicsFixture {
	t.Helper()
	clock := newTestChatClock()
	dbPath := filepath.Join(t.TempDir(), "chat-topics-test.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	if err := conversation.ApplyConversationSchema(ctx, db, migrate.SQLiteEmitter{}, clock, "", ""); err != nil {
		t.Fatalf("ApplyConversationSchema: %v", err)
	}
	store := conversation.NewStore(db)

	kv := storetest.NewMemStore() // real reference provider.Store (internal/storage/storetest's own doc: not a stub)
	bus := events.New(kv, clock)

	threadStore, err := topics.NewConversationThreadStore(store, clock)
	if err != nil {
		t.Fatalf("NewConversationThreadStore: %v", err)
	}
	taxonomy := topics.NewTaxonomyConfig(map[string]topics.TopicType{"topicB": "topicB"}, "general")
	exemplars, err := topics.NewExemplarStore(kv, clock, 0)
	if err != nil {
		t.Fatalf("NewExemplarStore: %v", err)
	}
	executor := &fakeSegmenterExecutor{failAfter: failAfter}
	threader, err := topics.NewDefaultAutoThreader(
		executor, fakeSegmenterEmbedder{}, topics.HysteresisConfig{Threshold: 0.5, Window: 1},
		threadStore, taxonomy, exemplars, bus, clock)
	if err != nil {
		t.Fatalf("NewDefaultAutoThreader: %v", err)
	}
	observer, err := topics.NewObserveLogger(threader, kv, clock, bus)
	if err != nil {
		t.Fatalf("NewObserveLogger: %v", err)
	}
	engine := &chatTopicEngine{logger: slog.Default(), observer: observer}

	adapter := conversation.NewAdapter(store, bus, nil, clock, "")
	adapter.SetTopicObserver(engine)
	registry := rpc.NewRegistry()
	adapter.RegisterHandlers(registry)
	topicsHandlers, err := topics.NewRPCHandlers(store, clock)
	if err != nil {
		t.Fatalf("topics.NewRPCHandlers: %v", err)
	}
	topicsHandlers.RegisterHandlers(registry)

	return &chatTopicsFixture{t: t, clock: clock, store: store, bus: bus, registry: registry, engine: engine}
}

// appendTurn dispatches chat.append_turn through the real registry and
// decodes its result, failing the test on any RPC error.
func (f *chatTopicsFixture) appendTurn(threadID, text string) appendTurnResultForTest {
	f.t.Helper()
	params, _ := json.Marshal(map[string]any{
		"thread_id": threadID, "role": "user",
		"segments": []map[string]string{{"kind": "text", "content": text}},
	})
	result, errObj := f.registry.Dispatch(context.Background(),
		&rpc.Request{JSONRPC: "2.0", Method: conversation.MethodAppendTurn, Params: params})
	if errObj != nil {
		f.t.Fatalf("chat.append_turn(%q): %+v", text, errObj)
	}
	b, _ := json.Marshal(result)
	var out appendTurnResultForTest
	if err := json.Unmarshal(b, &out); err != nil {
		f.t.Fatalf("decode append result: %v", err)
	}
	return out
}

// appendTurnResultForTest mirrors conversation's unexported wire shape
// (its own type is unexported to that package) so this file can decode
// the real JSON-RPC response chat.append_turn produces.
type appendTurnResultForTest struct {
	ThreadID     string `json:"thread_id"`
	TurnID       string `json:"turn_id"`
	Seq          int64  `json:"seq"`
	TopicWarning string `json:"topic_warning"`
}
