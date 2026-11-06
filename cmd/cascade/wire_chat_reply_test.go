//go:build !windows && integration

// Purpose: prove the chat.reply SSE topic serves a reply stream only to the
// caller it is bound to, and that stream mode works end to end, over the
// real rpc.Handler, SSEMux, chat.* adapter and Replier on a unix socket.
// SPORT: cmd/cascade chat reply topic (TEST) — P1-CPA-13c.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/conversation"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/provider"
)

// replyWordCounter counts whitespace-separated words.
type replyWordCounter struct{}

func (replyWordCounter) Count(_ context.Context, s string) (int, error) {
	return len(strings.Fields(s)), nil
}

// replyScriptDoor answers context stages in-process and streams parts.
type replyScriptDoor struct{ parts []string }

func (d replyScriptDoor) Execute(_ context.Context, req provider.ModelRequest) (provider.ModelResponse, error) {
	if req.TaskClass == "summarize" && len(req.Inputs) > 0 {
		return provider.ModelResponse{Output: req.Inputs[0].Content}, nil
	}
	return provider.ModelResponse{Output: "label"}, nil
}

func (d replyScriptDoor) ExecuteStream(context.Context, provider.ModelRequest) (<-chan provider.StreamEvent, func(), error) {
	ch := make(chan provider.StreamEvent, len(d.parts)+1)
	for _, p := range d.parts {
		ch <- provider.StreamEvent{Kind: provider.StreamEventDelta, Delta: p}
	}
	ch <- provider.StreamEvent{Kind: provider.StreamEventDone}
	close(ch)
	return ch, func() {}, nil
}

// mustReply fails t on a non-nil err.
func mustReply(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

type replyTopicFixture struct {
	socket, token string
	registry      *rpc.Registry
	owner         *client.Client
	hub           *conversation.ReplyHub
}

// newReplyTopicFixture composes the chat.reply surface the daemon serves.
func newReplyTopicFixture(t *testing.T, parts ...string) *replyTopicFixture {
	t.Helper()
	t.Setenv(principal.EnvToken, "")
	mustReply(t, os.Unsetenv(principal.EnvToken)) // the client binds the owner CLI only when unset
	ctx := context.Background()
	dir, err := os.MkdirTemp("", "crt") // short: a unix socket path is bounded
	mustReply(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	paths, err := runtime.NewPathProvider(func(string) string { return "" }, func() (string, error) { return dir, nil })
	mustReply(t, err)
	clock := runtime.NewFixedClock(time.Unix(1_700_000_000, 0))
	minter := principal.NewMinter(clock)
	token, err := minter.Mint(principal.Principal{ID: "other", Kind: principal.KindSession,
		SessionID: "s-other", Scope: "project:test"}, time.Hour)
	mustReply(t, err)
	registry := rpc.NewRegistry()
	registry.SetAccessPolicy(func(tok string) (principal.Principal, error) { return minter.Resolve(tok, clock.Now()) })
	w := &daemonWiring{Ctx: ctx, Registry: registry, Events: rpc.NewSSEMux(nil), Clock: clock}
	wireRegistrationNamed(t, w, "chat-reply-topic")
	hub, err := chatReplyHub(w)
	mustReply(t, err)
	db, convStore, err := openChatConversationStore(ctx, paths, clock)
	mustReply(t, err)
	t.Cleanup(func() { _ = db.Close() })
	adapter := conversation.NewAdapter(convStore, events.New(storetest.NewMemStore(), clock), nil, clock, chatDaemonMode)
	js, err := wireChatJournal(ctx, adapter, convStore, storetest.NewMemStore(), clock)
	mustReply(t, err)
	adapter.RegisterHandlers(registry)
	effects, err := audit.NewEffectLog(audit.New(storetest.NewMemStore(), clock, nil))
	mustReply(t, err)
	mustReply(t, wireChatReply(ctx, registry, convStore, js, replyScriptDoor{parts: parts},
		replyWordCounter{}, effects, hub, clock))
	socket := filepath.Join(dir, "d.sock")
	ln, err := net.Listen("unix", socket)
	mustReply(t, err)
	srv := &http.Server{Handler: rpc.NewHandlerWithSSE(registry, w.Events), ConnContext: rpc.ConnContext}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return &replyTopicFixture{socket: socket, registry: registry, token: token, hub: hub,
		owner: client.New(socket, client.UnixDialer, 10*time.Second)}
}

// wireRegistrationNamed runs the daemon registration called name on w.
func wireRegistrationNamed(t *testing.T, w *daemonWiring, name string) {
	t.Helper()
	for _, r := range daemonRegistrations {
		if r.Name == name {
			if err := r.Wire(w); err != nil {
				t.Fatal(err)
			}
			return
		}
	}
	t.Fatalf("no daemon registration %q", name)
}

// openOwnerStream appends a user turn as the owner CLI and opens its stream.
func (f *replyTopicFixture) openOwnerStream(t *testing.T) string {
	t.Helper()
	var turn struct {
		ThreadID string `json:"thread_id"`
		TurnID   string `json:"turn_id"`
	}
	params := map[string]any{"role": "user", "privacy_mode": "restricted",
		"segments": []map[string]string{{"kind": "text", "content": "say hello"}}}
	mustReply(t, f.owner.Do(context.Background(), conversation.MethodAppendTurn, params, &turn))
	var opened struct {
		StreamID string `json:"stream_id"`
	}
	mustReply(t, f.owner.Do(context.Background(), "chat.reply_open", map[string]string{"thread_id": turn.ThreadID, "turn_id": turn.TurnID}, &opened))
	return opened.StreamID
}

// get issues GET path with the given caller headers and returns the response.
func (f *replyTopicFixture) get(t *testing.T, path, token, marker string) *http.Response {
	t.Helper()
	hc := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return client.UnixDialer(ctx, f.socket)
	}}}
	t.Cleanup(hc.CloseIdleConnections)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://unix"+path, nil)
	mustReply(t, err)
	if token != "" {
		req.Header.Set(principal.HeaderToken, token)
	}
	if marker != "" {
		req.Header.Set(principal.HeaderOwnerCLI, marker)
	}
	resp, err := hc.Do(req)
	mustReply(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// readFrames reads SSE data records until the terminal frame or EOF.
func readFrames(t *testing.T, resp *http.Response) []conversation.Frame {
	t.Helper()
	var frames []conversation.Frame
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok {
			continue
		}
		var f conversation.Frame
		mustReply(t, json.Unmarshal([]byte(data), &f))
		if frames = append(frames, f); f.Type != conversation.FrameDelta {
			break
		}
	}
	return frames
}

func streamPath(key, id string) string {
	return "/events?" + url.Values{"topic": {"chat.reply"}, key: {id}}.Encode()
}

// TestReplyTopicRefusesOtherCallersStream: a stream bound to the owner CLI
// is refused, 403 with zero frames, to every other caller identity.
func TestReplyTopicRefusesOtherCallersStream(t *testing.T) {
	f := newReplyTopicFixture(t, "hel", "lo")
	id := f.openOwnerStream(t)
	// The predicate admits every session, so only the binding can refuse.
	f.registry.SetEventVisibleFactory(func(principal.Principal) func(string, string) bool {
		return func(string, string) bool { return true }
	})
	for name, caller := range map[string][2]string{"principal": {f.token, ""}, "owner-model": {"", "owner-model"}} {
		resp := f.get(t, streamPath("stream", id), caller[0], caller[1])
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s: status %d, want 403", name, resp.StatusCode)
		}
		if frames := readFrames(t, resp); len(frames) != 0 {
			t.Fatalf("%s: got %d frames from another caller's stream", name, len(frames))
		}
	}
	// The predicate refuses on its own even where the binding admits.
	rec := httptest.NewRecorder()
	ctx := rpc.WithEventVisible(principal.WithCaller(context.Background(), principal.Caller{OwnerCLI: true}), nil)
	req := (&http.Request{Method: http.MethodGet, URL: &url.URL{RawQuery: "stream_id=" + id}}).WithContext(ctx)
	(&chatReplyTopic{hub: f.hub}).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), "data:") {
		t.Fatalf("hidden stream served: %d %q", rec.Code, rec.Body.String())
	}
	resp := f.get(t, streamPath("stream_id", id), "", "owner-cli")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("bound caller refused after refusals: %d", resp.StatusCode)
	}
}

// TestReplyStreamServedOverRealSSEHandler: the bound caller gets every
// delta and the terminal frame through the real handler.
func TestReplyStreamServedOverRealSSEHandler(t *testing.T) {
	f := newReplyTopicFixture(t, "hel", "lo")
	id := f.openOwnerStream(t)
	resp := f.get(t, streamPath("stream_id", id), "", "owner-cli")
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("status %d %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	var res conversation.ReplyResult
	if err := f.owner.Do(context.Background(), conversation.MethodReply, map[string]string{"stream_id": id}, &res); err != nil {
		t.Fatalf("chat.reply: %v", err)
	}
	frames := readFrames(t, resp)
	var text strings.Builder
	for _, fr := range frames[:len(frames)-1] {
		text.WriteString(fr.Delta)
	}
	if last := frames[len(frames)-1]; last.Type != conversation.FrameDone || last.ThreadID != res.ThreadID || text.String() != "hello" {
		t.Fatalf("frames %+v, result %+v", frames, res)
	}
}

// TestP1ChatReplyStreamEvents: stream mode opens a stream, subscribes
// ?topic=chat.reply&stream=<id>, calls chat.reply, renders the deltas and
// reads the durable assistant turn after the terminal frame.
func TestP1ChatReplyStreamEvents(t *testing.T) {
	f := newReplyTopicFixture(t, "stream", "ed reply")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	id := f.openOwnerStream(t)
	stream, closeStream, err := f.owner.StreamTopic(ctx, "chat.reply", url.Values{"stream": {id}})
	mustReply(t, err)
	defer closeStream()
	var res conversation.ReplyResult
	replied := make(chan error, 1)
	go func() { replied <- f.owner.Do(ctx, conversation.MethodReply, map[string]string{"stream_id": id}, &res) }()
	var rendered strings.Builder
	for terminal := false; !terminal; {
		ev, ok := <-stream
		if !ok {
			t.Fatal("stream ended before the terminal frame")
		}
		var fr conversation.Frame
		mustReply(t, json.Unmarshal([]byte(ev.Data), &fr))
		switch fr.Type {
		case conversation.FrameDelta:
			rendered.WriteString(fr.Delta)
		case conversation.FrameDone:
			terminal = true
		default:
			t.Fatalf("unexpected frame %+v", fr)
		}
	}
	mustReply(t, <-replied)
	var history struct {
		Turns []struct {
			Turn     conversation.Turn      `json:"turn"`
			Segments []conversation.Segment `json:"segments"`
		} `json:"turns"`
	}
	mustReply(t, f.owner.Do(ctx, conversation.MethodGetThread, map[string]string{"thread_id": res.ThreadID}, &history))
	var durable strings.Builder
	for _, tw := range history.Turns {
		for _, seg := range tw.Segments {
			if tw.Turn.ID == res.AssistantTurnID && seg.Kind == conversation.SegmentText {
				durable.WriteString(seg.Content)
			}
		}
	}
	if rendered.String() != "streamed reply" || durable.String() != rendered.String() {
		t.Fatalf("rendered %q, durable %q", rendered.String(), durable.String())
	}
}
