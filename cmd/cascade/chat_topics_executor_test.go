package main

// Purpose: P1-CPA-12's topic-engine gate and executor tests: the thread
//   tier reaches every classifier request, and each skip (unreadable tier,
//   a Selection that does not admit the tier, a classifier lane whose
//   locality does not admit it, no classifier lane) makes zero door calls.
//   Each zero-call case has a positive control on the same engine, so a
//   gate that never calls anything cannot pass.
// Constraints: real sqlite conversation store, real topics composition;
//   only the conductor door (the provider seam) is a recording double.

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/conversation"
	"github.com/acamarata/cascade/internal/conversation/topics"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/retrieval/embedsel"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// recordingClassifierDoor records the tier of every Execute it receives,
// both from the request and from the ctx, and labels by "topicA".
type recordingClassifierDoor struct {
	mu       sync.Mutex
	reqTiers []provider.SensitivityTier
	ctxTiers []provider.SensitivityTier
}

func (d *recordingClassifierDoor) Execute(ctx context.Context, req provider.ModelRequest) (provider.ModelResponse, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	ctxTier, _ := conductor.DoorSensitivityFrom(ctx)
	d.reqTiers = append(d.reqTiers, req.Sensitivity)
	d.ctxTiers = append(d.ctxTiers, ctxTier)
	label := "topicB"
	if strings.Contains(req.Inputs[0].Content, "topicA") {
		label = "topicA"
	}
	return provider.ModelResponse{Output: label}, nil
}

// snapshot returns the request tiers recorded since the last call.
func (d *recordingClassifierDoor) snapshot() []provider.SensitivityTier {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := d.reqTiers
	for i, ct := range d.ctxTiers {
		if ct != out[i] {
			out = append([]provider.SensitivityTier(nil), out...)
			out[i] = provider.SensitivityTier(255) // a ctx/request mismatch reads as invalid
		}
	}
	d.reqTiers, d.ctxTiers = nil, nil
	return out
}

type topicGateFixture struct {
	engine *chatTopicEngine
	door   *recordingClassifierDoor
	store  conversation.Store
	db     *sql.DB
	clock  *mutableClock
}

// newTopicGateFixture builds the engine through newChatTopicEngineWith over
// deps, with the recording door and an in-memory retrieval store.
func newTopicGateFixture(t *testing.T, deps chatTopicDeps) *topicGateFixture {
	t.Helper()
	clock := newTestChatClock()
	store, db := openTestConversationStore(t, clock)
	kv := storetest.NewMemStore()
	bus := events.New(kv, clock)
	door := &recordingClassifierDoor{}
	deps.door, deps.retrieval = door, kv
	engine, err := newChatTopicEngineWith(store, clock, bus, deps)
	if err != nil {
		t.Fatalf("newChatTopicEngineWith: %v", err)
	}
	registry := rpc.NewRegistry()
	conversation.NewAdapter(store, bus, nil, clock, "").RegisterHandlers(registry)
	applyThreadScopeSchema(context.Background(), t, registry)
	return &topicGateFixture{engine: engine, door: door, store: store, db: db, clock: clock}
}

// observe creates threadID at tier and observes a two-topic window on it.
func (f *topicGateFixture) observe(t *testing.T, threadID string, tier provider.SensitivityTier) string {
	t.Helper()
	if err := f.store.CreateThread(context.Background(), threadID, tier, "owner", f.clock.Now().Unix()); err != nil {
		t.Fatalf("CreateThread(%s): %v", threadID, err)
	}
	return f.engine.Observe(context.Background(), threadID, []conversation.TopicWindowTurn{
		{Role: "user", Text: "topicA opening line in " + threadID}, {Role: "user", Text: "topicB switch line in " + threadID},
	})
}

func laneDeps(sel *embedsel.Selection, lt conductor.LaneType) chatTopicDeps {
	d := chatTopicDeps{selection: sel, laneType: fixedLaneType(lt),
		taxonomy: topics.NewTaxonomyConfig(map[string]topics.TopicType{"topicA": "topicA", "topicB": "topicB"}, "general")}
	d.settings.ClassifierLane, d.settings.ClassifierModel = "topics-classifier", "m"
	return d
}

// laneSelection2 is a conductor-lane-shaped Selection: internal and public.
func laneSelection2() *embedsel.Selection {
	return embedsel.New(fakeSegmenterEmbedder{}, embedsel.SourceConductorLane, "m", 2,
		[]provider.SensitivityTier{provider.SensitivityInternal, provider.SensitivityPublic})
}

func wantSkip(t *testing.T, f *topicGateFixture, warning, reason string) {
	t.Helper()
	if !strings.HasPrefix(warning, "topics: skipped (") || !strings.Contains(warning, reason) {
		t.Fatalf("warning = %q, want a skip naming %q", warning, reason)
	}
	if got := f.door.snapshot(); len(got) != 0 {
		t.Fatalf("door calls = %d on a skipped turn, want 0", len(got))
	}
}

func wantCalledAt(t *testing.T, f *topicGateFixture, warning string, tier provider.SensitivityTier) {
	t.Helper()
	if warning != "" {
		t.Fatalf("warning = %q, want none", warning)
	}
	got := f.door.snapshot()
	if len(got) == 0 {
		t.Fatalf("no door call at tier %s", tier)
	}
	for i, g := range got {
		if g != tier {
			t.Fatalf("call %d sensitivity = %s, want the thread tier %s on request and ctx", i, g, tier)
		}
	}
}

func TestChatTopicEngineStampsThreadTier(t *testing.T) {
	f := newTopicGateFixture(t, laneDeps(laneSelection2(), conductor.LaneExternalAPI))
	wantCalledAt(t, f, f.observe(t, "th-internal", provider.SensitivityInternal), provider.SensitivityInternal)
	wantCalledAt(t, f, f.observe(t, "th-public", provider.SensitivityPublic), provider.SensitivityPublic)
}

func TestChatTopicEngineSkipsUnreadableTier(t *testing.T) {
	f := newTopicGateFixture(t, laneDeps(allTierSelection(), conductor.LaneControllerLocal))
	wantCalledAt(t, f, f.observe(t, "th-ok", provider.SensitivityInternal), provider.SensitivityInternal)
	if err := f.db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}
	warning := f.engine.Observe(context.Background(), "th-ok", []conversation.TopicWindowTurn{{Role: "user", Text: "topicA"}})
	wantSkip(t, f, warning, "thread tier unreadable")
}

func TestChatTopicEngineSkipsLocalOnlyThreadOnLane(t *testing.T) {
	f := newTopicGateFixture(t, laneDeps(laneSelection2(), conductor.LaneControllerLocal))
	wantSkip(t, f, f.observe(t, "th-local", provider.SensitivityLocalOnly), "embedder selection does not admit tier local-only")
	wantSkip(t, f, f.observe(t, "th-restricted", provider.SensitivityRestricted), "does not admit tier restricted")
	wantCalledAt(t, f, f.observe(t, "th-internal", provider.SensitivityInternal), provider.SensitivityInternal)
}

func TestChatTopicEngineLocalOnlyThreadRemoteClassifierZeroCalls(t *testing.T) {
	f := newTopicGateFixture(t, laneDeps(allTierSelection(), conductor.LaneExternalAPI))
	if !f.engine.selection.Admits(provider.SensitivityLocalOnly) {
		t.Fatal("precondition: the sidecar Selection must admit local-only")
	}
	wantSkip(t, f, f.observe(t, "th-local", provider.SensitivityLocalOnly),
		"classifier lane topics-classifier (external-api) does not admit tier local-only")
	wantSkip(t, f, f.observe(t, "th-restricted", provider.SensitivityRestricted), "(external-api) does not admit tier restricted")
	wantCalledAt(t, f, f.observe(t, "th-internal", provider.SensitivityInternal), provider.SensitivityInternal)
}

func TestChatTopicEngineSkipsWithoutClassifierLane(t *testing.T) {
	deps := laneDeps(allTierSelection(), conductor.LaneControllerLocal)
	deps.settings.ClassifierLane, deps.settings.ClassifierModel = "", ""
	f := newTopicGateFixture(t, deps)
	warning := f.observe(t, "th-internal", provider.SensitivityInternal)
	if !strings.Contains(warning, "not configured (no classifier lane configured") {
		t.Fatalf("warning = %q, want it to name the missing classifier lane", warning)
	}
	if got := f.door.snapshot(); len(got) != 0 {
		t.Fatalf("door calls = %d with no classifier lane, want 0", len(got))
	}
	on := newTopicGateFixture(t, laneDeps(allTierSelection(), conductor.LaneControllerLocal))
	wantCalledAt(t, on, on.observe(t, "th-internal", provider.SensitivityInternal), provider.SensitivityInternal)
}

func TestChatTopicEngineConfiguredWithSelection(t *testing.T) {
	paths := embedTestPaths(t)
	loadEmbedConfig(t, paths, laneBody+"[topics]\nclassifier_lane = \"topics-classifier\"\nclassifier_model = \"gemini-2.5-flash\"\n")
	clock := newTestChatClock()
	store, _ := openTestConversationStore(t, clock)
	engine, err := newChatTopicEngine(store, clock, events.New(storetest.NewMemStore(), clock), paths)
	if err != nil {
		t.Fatalf("newChatTopicEngine: %v", err)
	}
	recallSel := processEmbedder(paths) // the recall vector leg's Selection
	if engine.selection == nil || engine.selection != recallSel {
		t.Fatalf("topic engine Selection %p, recall vector leg %p: want the same pointer", engine.selection, recallSel)
	}
	if recallSel.Source() != embedsel.SourceConductorLane || engine.lane != "topics-classifier" {
		t.Fatalf("source %s lane %q, want the configured conductor lane and classifier lane", recallSel.Source(), engine.lane)
	}
	if other := processEmbedder(embedTestPaths(t)); other == engine.selection {
		t.Fatal("a second config path shares the Selection; the identity check above proves nothing")
	}
}

func TestChatModelExecutorRefusesUnstampedTier(t *testing.T) {
	door := &recordingClassifierDoor{}
	exec := chatModelExecutor{door: door, lane: "topics-classifier", model: "m"}
	_, err := exec.Execute(context.Background(), provider.ModelRequest{Inputs: []provider.ChatMessage{{Content: "topicA"}}})
	var ce *cascade.Error
	if !errors.As(err, &ce) || ce.Kind != cascade.KindPolicyDenied ||
		ce.Msg != `topics: classifier lane "topics-classifier" (model "m"): no thread tier on the request; refusing to classify` {
		t.Fatalf("err = %v, want the KindPolicyDenied unstamped-tier refusal", err)
	}
	if len(door.snapshot()) != 0 {
		t.Fatal("an unstamped request reached the door")
	}
}
