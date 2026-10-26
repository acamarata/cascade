//go:build !windows && integration

package conversation

// Purpose: the V/S-47.T2 real-counterpart proof (Art.2). A REAL daemon
//   (internal/daemon.Run), a REAL unix socket and a REAL net/http client:
//   chat.create_thread makes the topic thread, chat.append_turn seeds it
//   (its genuine SSE CLIENT-LOCAL ECHO observed), chat.create_thread makes
//   the new session's thread, chat.resume composes the topic INTO the
//   session through a real vault-backed egress.Engine, and the session's
//   next chat.append_turn carries the block as leading_context -- read
//   back with chat.get_thread as segment 0 of that turn. STEP 3 compares
//   the live chat.resume result with testdata/resume-fixtures both ways.
//   A real-socket test cannot share a file with the untagged unit tests
//   (the default lane forbids importing net), hence the integration tag.
// SPORT: internal.conversation.resume/ADDED (integration test)
//   (P1-E22-W5-S47-T2).

import (
	"bufio"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	cascadecontext "github.com/acamarata/cascade/internal/context"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/provider"
)

// TestResume_RealSocket_FullFlow drives create -> seed -> create session
// -> resume -> next append against a live daemon over a real unix socket.
func TestResume_RealSocket_FullFlow(t *testing.T) {
	clock := runtime.NewSystemClock()
	bus := events.New(storetest.NewMemStore(), clock)
	store := newTestStore(t)
	adapter := NewAdapter(store, bus, passthroughSubst{}, clock, "")
	registry := rpc.NewRegistry()
	adapter.RegisterHandlers(registry)
	registry.SetAccessPolicy(nil) // owner-marked requests only; no principal tokens in this proof
	composer, err := cascadecontext.NewComposer(provider.NaiveTokenCounter{}, nil)
	if err != nil {
		t.Fatalf("NewComposer: %v", err)
	}
	resumeSvc, err := NewResumer(store, composer, nil, newResumeIntegrationEgressEngine(t, t.TempDir()))
	if err != nil {
		t.Fatalf("NewResumer: %v", err)
	}
	adapter.SetResume(resumeSvc)
	client := startResumeDaemon(t, registry, bus, clock)

	// STEP 1: an internal topic thread, seeded with a turn that carries the
	// vaulted value in the clear; its SSE echo proves the live append path.
	var topic createThreadResult
	postRPC(t, client, MethodCreateThread, map[string]string{"privacy_mode": "internal"}, &topic)
	echo := subscribeEcho(t, client)
	var seeded appendTurnResult
	postRPC(t, client, MethodAppendTurn, map[string]any{"thread_id": topic.ThreadID, "role": "user",
		"segments": []map[string]string{{"kind": "text", "content": "integration test turn " + resumeVaultedName + "=" + resumeVaultedValue}}}, &seeded)
	select {
	case <-echo:
	case <-time.After(10 * time.Second):
		t.Fatal("SSE echo for the seeded turn never arrived within 10s")
	}

	// STEP 2: a new session thread (restricted -- the no-flag default),
	// resumed INTO; the internal source is less restrictive, so admitted.
	var session createThreadResult
	postRPC(t, client, MethodCreateThread, map[string]string{"privacy_mode": "restricted"}, &session)
	var resumed resumeResult
	postRPC(t, client, MethodResume, map[string]string{"topic_id": topic.ThreadID, "session_thread_id": session.ThreadID}, &resumed)
	if resumed.ThreadID != topic.ThreadID || !strings.Contains(resumed.LeadingContext, "integration test turn") {
		t.Fatalf("chat.resume = %+v, want the topic thread's content", resumed)
	}
	if strings.Contains(resumed.LeadingContext, resumeVaultedValue) {
		t.Fatalf("chat.resume leaked the vaulted value: %q", resumed.LeadingContext)
	}

	// The leading block observed on the client's next request.
	assertLeadingBlockDelivered(t, client, session.ThreadID, resumed.LeadingContext)
	compareResumeFixture(t, resumed)
}

// subscribeEcho opens the SSE stream and returns a channel closed when the
// first data line arrives.
func subscribeEcho(t *testing.T, client *http.Client) <-chan struct{} {
	t.Helper()
	resp, err := client.Get("http://unix" + rpc.EventsPath)
	if err != nil {
		t.Fatalf("GET %s: %v", rpc.EventsPath, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	arrived := make(chan struct{})
	go func() {
		reader := bufio.NewReader(resp.Body)
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			if strings.HasPrefix(line, "data:") {
				close(arrived)
				return
			}
		}
	}()
	return arrived
}

// assertLeadingBlockDelivered sends the session's next turn with the block
// as leading_context and reads the thread back: the block must be segment
// 0 of that turn, ahead of the prompt.
func assertLeadingBlockDelivered(t *testing.T, client *http.Client, sessionID, block string) {
	t.Helper()
	var next appendTurnResult
	postRPC(t, client, MethodAppendTurn, map[string]any{"thread_id": sessionID, "role": "user", "leading_context": block,
		"segments": []map[string]string{{"kind": "text", "content": "my next prompt"}}}, &next)
	var thread struct {
		Turns []struct {
			Turn     Turn      `json:"turn"`
			Segments []Segment `json:"segments"`
		} `json:"turns"`
	}
	postRPC(t, client, MethodGetThread, map[string]string{"thread_id": sessionID}, &thread)
	if len(thread.Turns) != 1 || thread.Turns[0].Turn.ID != next.TurnID {
		t.Fatalf("session thread = %+v, want exactly the one appended turn", thread)
	}
	segs := thread.Turns[0].Segments
	if len(segs) != 2 || segs[0].Content != block || segs[1].Content != "my next prompt" {
		t.Fatalf("segments = %+v, want [resumed block, prompt]", segs)
	}
}

// compareResumeFixture is STEP 3: the fixture documents chat.resume's wire
// SHAPE (testdata/README.md). -update rewrites it from this live result;
// the default run compares the field sets both ways.
func compareResumeFixture(t *testing.T, live resumeResult) {
	t.Helper()
	fixturePath := filepath.Join("testdata", "resume-fixtures", "example-chat-resume-response.json")
	liveRaw, err := json.Marshal(live)
	if err != nil {
		t.Fatalf("marshal live resumeResult: %v", err)
	}
	var liveFields map[string]json.RawMessage
	if err := json.Unmarshal(liveRaw, &liveFields); err != nil {
		t.Fatalf("unmarshal live resumeResult: %v", err)
	}
	if *updateResumeFixture {
		writeResumeFixture(t, fixturePath, liveFields)
		return
	}
	fixtureBytes, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("read resume fixture: %v", err)
	}
	var fixtureFields map[string]json.RawMessage
	if err := json.Unmarshal(fixtureBytes, &fixtureFields); err != nil {
		t.Fatalf("unmarshal resume fixture: %v", err)
	}
	delete(fixtureFields, "_provenance")
	for field := range liveFields {
		if _, ok := fixtureFields[field]; !ok {
			t.Errorf("resume fixture is missing live response field %q", field)
		}
	}
	for field := range fixtureFields {
		if _, ok := liveFields[field]; !ok {
			t.Errorf("resume fixture carries field %q the live response no longer has; rerun with -update", field)
		}
	}
}
