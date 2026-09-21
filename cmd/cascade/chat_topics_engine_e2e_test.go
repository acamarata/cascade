package main

// Purpose: the D6 test cases -- observe mode's audit event, apply mode's
//   real thread filing after the 7-day gate, an executor failure
//   surfacing as topic_warning without failing the append, and the
//   not-configured engine's own warning shape. Split from chat_topics_
//   engine_test.go (fixture/doubles) for Art.10.3's 300-line cap.
// SPORT: cmd/cascade chat topics engine (ADD tests) -- P1-E21-W5-S46-T4 D2.

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/conversation/topics"
	"github.com/acamarata/cascade/internal/doctor"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/storage"
)

// TestChatTopicEngine_ObserveThenAppliesAfterSevenDays is the D6 end-to-
// end case: two turns appended inside the 7-day observe window produce a
// topics.observe_log audit event and no filed thread; the same window
// re-delivered after the clock crosses 7 days routes for real (apply
// mode), and chat.topics_list reports the filed thread.
func TestChatTopicEngine_ObserveThenAppliesAfterSevenDays(t *testing.T) {
	f := newChatTopicsFixture(t, 0)

	first := f.appendTurn("th-observe", "topicA opening line")
	if first.TopicWarning != "" {
		t.Fatalf("observe mode: TopicWarning = %q, want empty", first.TopicWarning)
	}
	second := f.appendTurn("th-observe", "topicB switch line")
	if second.TopicWarning != "" {
		t.Fatalf("observe mode: TopicWarning = %q, want empty", second.TopicWarning)
	}

	events, err := f.bus.Replay(context.Background(), string(storage.DomainAudit), 0)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	observeCount := 0
	for _, e := range events {
		if e.Kind == topics.EventKindTopicObserve {
			observeCount++
		}
	}
	if observeCount == 0 {
		t.Fatalf("observe mode: no %s events published, got %d total events", topics.EventKindTopicObserve, len(events))
	}

	// No thread should exist yet: still inside the 7-day observe window.
	topicsBefore := f.listTopics(t)
	if len(topicsBefore) != 0 {
		t.Fatalf("observe mode: topics_list = %+v, want none filed yet", topicsBefore)
	}

	// Cross the 7-day gate and re-deliver the same window (RE-DELIVERY IS
	// IDEMPOTENT, auto_thread.go's own contract) via a third append that
	// extends it.
	f.clock.now = f.clock.now.Add(8 * 24 * time.Hour)
	third := f.appendTurn("th-observe", "topicB continues")
	if third.TopicWarning != "" {
		t.Fatalf("apply mode: TopicWarning = %q, want empty", third.TopicWarning)
	}

	topicsAfter := f.listTopics(t)
	if len(topicsAfter) == 0 {
		t.Fatalf("apply mode: chat.topics_list reported no threads after the 7-day gate")
	}
	found := false
	for _, tp := range topicsAfter {
		if tp == "topicB" {
			found = true
		}
	}
	if !found {
		t.Fatalf("apply mode: chat.topics_list = %+v, want a topicB entry", topicsAfter)
	}
}

// listTopics dispatches chat.topics_list and returns the topic_type
// column of every row.
func (f *chatTopicsFixture) listTopics(t *testing.T) []string {
	t.Helper()
	result, errObj := f.registry.Dispatch(context.Background(),
		&rpc.Request{JSONRPC: "2.0", Method: topics.MethodTopicsList, Params: nil})
	if errObj != nil {
		t.Fatalf("chat.topics_list: %+v", errObj)
	}
	b, _ := json.Marshal(result)
	var decoded struct {
		Topics []struct {
			TopicType string `json:"topic_type"`
		} `json:"topics"`
	}
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("decode chat.topics_list: %v", err)
	}
	out := make([]string, len(decoded.Topics))
	for i, tp := range decoded.Topics {
		out[i] = tp.TopicType
	}
	return out
}

// TestChatTopicEngine_ExecutorErrorSurfacesAsWarning_AppendStillSucceeds
// is the D6 error case: a classify dispatch failure (the segmenter's
// provider.ModelExecutor) must surface as appendTurnResult.TopicWarning,
// never as a chat.append_turn RPC error -- the turn is already durably
// stored by the time Observe runs (adapter_topics.go's own contract).
func TestChatTopicEngine_ExecutorErrorSurfacesAsWarning_AppendStillSucceeds(t *testing.T) {
	f := newChatTopicsFixture(t, 1) // fail on the very first classify call

	result := f.appendTurn("th-error", "topicA line that will fail to classify")
	if result.TurnID == "" {
		t.Fatalf("append_turn result has no turn_id: %+v", result)
	}
	if result.TopicWarning == "" {
		t.Fatalf("TopicWarning = %q, want a non-empty warning naming the executor failure", result.TopicWarning)
	}
	if !strings.Contains(result.TopicWarning, "observe failed") {
		t.Fatalf("TopicWarning = %q, want it to name the observe failure", result.TopicWarning)
	}

	// The turn itself is durably stored regardless of the engine failure.
	thread, ok, err := f.store.GetThread(context.Background(), "th-error")
	if err != nil || !ok {
		t.Fatalf("GetThread(th-error) = %v, %v, %v, want a stored thread", thread, ok, err)
	}
}

// TestChatTopicEngine_NotConfigured proves the typed "not configured"
// state: an engine with a nil observer reports a fixed, non-empty warning
// on every Observe call and never panics on a nil dereference.
func TestChatTopicEngine_NotConfigured(t *testing.T) {
	engine := &chatTopicEngine{logger: slog.Default(), reason: "no production embedding provider is composed"}
	warning := engine.Observe(context.Background(), "th-any", nil)
	if warning == "" || !strings.Contains(warning, "not configured") {
		t.Fatalf("Observe on a not-configured engine = %q, want it to say 'not configured'", warning)
	}
}

// TestChatTopicsDoctorCheck_NotConfiguredIsOK proves the doctor check
// treats "no embedder configured" as StatusOK, matching internal/doctor/
// checks_provider.go's identical "no providers registered" verdict --
// see chat_topics_doctor.go's header for why StatusWarn here would fail
// TestDoctorIsMountedOnRoot's bare-install reachability proof.
func TestChatTopicsDoctorCheck_NotConfiguredIsOK(t *testing.T) {
	check := newChatTopicsDoctorCheck(nil)
	result, err := check.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != doctor.StatusOK {
		t.Fatalf("Status = %q, want ok (not-configured is the healthy default)", result.Status)
	}
	if result.Detail == "" {
		t.Fatalf("Detail is empty, want the resolver's disclosed reason")
	}
	if _, err := check.Fix(context.Background()); err == nil {
		t.Fatalf("Fix: want ErrCheckNotFixable, got nil error")
	}
}
