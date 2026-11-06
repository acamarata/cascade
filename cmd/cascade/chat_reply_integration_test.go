//go:build integration && !windows

// Purpose: replay a chat exchange through the composed chat.reply surface
//
//	(real rpc.Handler, SSEMux, chat.* adapter, journal, effect log and
//	Replier on a unix socket) with no network, and prove that cancelling
//	the client after the first token is a typed failure that stores no
//	assistant turn and reports no success.
//
// Inputs: testdata/chat_reply/recorded_exchange.json (P1-CPA-13d) when it is
//
//	committed; until then the inline replayExchange below, in the same
//	shape, is replayed instead. Neither path skips.
//
// Constraints: HOME, USERPROFILE and CASCADE_HOME live in t.TempDir(); the
//
//	only socket is a local unix socket; the door replays frames in-process.
//
// SPORT: cmd/cascade chat reply integration (TEST) — P1-CPA-13e.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
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
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// replayExchange is the subset of the recorded exchange the replay needs.
type replayExchange struct {
	Prompt string `json:"prompt"`
	Reply  string `json:"reply"`
}

// inlineReplayExchange stands in for the recording until it is committed.
var inlineReplayExchange = replayExchange{
	Prompt: "Reply with one short sentence: what is the capital of France?",
	Reply:  "The capital of France is Paris.",
}

// loadReplayExchange reads the committed recording, or the inline one.
func loadReplayExchange(t *testing.T) replayExchange {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", "chat_reply", "recorded_exchange.json"))
	if errors.Is(err, os.ErrNotExist) {
		return inlineReplayExchange
	}
	mustReply(t, err)
	var ex replayExchange
	mustReply(t, json.Unmarshal(body, &ex))
	if ex.Prompt == "" || ex.Reply == "" {
		t.Fatalf("recorded exchange lacks a prompt or a reply: %+v", ex)
	}
	return ex
}

// replayDoor streams the recorded reply word by word; with hold it stops
// after the first token until the call's context ends.
type replayDoor struct {
	parts []string
	hold  bool
}

func (d replayDoor) Execute(_ context.Context, req provider.ModelRequest) (provider.ModelResponse, error) {
	if req.TaskClass == "summarize" && len(req.Inputs) > 0 {
		return provider.ModelResponse{Output: req.Inputs[0].Content}, nil
	}
	return provider.ModelResponse{Output: "label"}, nil
}

func (d replayDoor) ExecuteStream(ctx context.Context, _ provider.ModelRequest) (<-chan provider.StreamEvent, func(), error) {
	ch := make(chan provider.StreamEvent)
	go func() {
		defer close(ch)
		for i, p := range d.parts {
			select {
			case ch <- provider.StreamEvent{Kind: provider.StreamEventDelta, Delta: p}:
			case <-ctx.Done():
				return
			}
			if d.hold && i == 0 {
				<-ctx.Done()
				return
			}
		}
		select {
		case ch <- provider.StreamEvent{Kind: provider.StreamEventDone}:
		case <-ctx.Done():
		}
	}()
	return ch, func() {}, nil
}

// splitReply splits text into word tokens that rejoin to text.
func splitReply(text string) []string {
	words := strings.SplitAfter(text, " ")
	if len(words) < 2 {
		return []string{text}
	}
	return words
}

// newReplayFixture composes the chat.reply surface the daemon serves, as
// newReplyTopicFixture does, over a replay door.
func newReplayFixture(t *testing.T, door replayDoor) *replyTopicFixture {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CASCADE_HOME", filepath.Join(home, ".cascade"))
	t.Setenv(principal.EnvToken, "")
	mustReply(t, os.Unsetenv(principal.EnvToken)) // the client binds the owner CLI only when unset
	ctx := context.Background()
	dir, err := os.MkdirTemp("", "crr") // short: a unix socket path is bounded
	mustReply(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	paths, err := runtime.NewPathProvider(func(string) string { return "" }, func() (string, error) { return dir, nil })
	mustReply(t, err)
	clock := runtime.NewFixedClock(time.Unix(1_700_000_000, 0))
	registry := rpc.NewRegistry()
	registry.SetAccessPolicy(func(string) (principal.Principal, error) {
		return principal.Principal{}, cascade.New(cascade.KindPermissionDenied, "no principal in the replay")
	})
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
	mustReply(t, wireChatReply(ctx, registry, convStore, js, door, replyWordCounter{}, effects, hub, clock))
	socket := filepath.Join(dir, "d.sock")
	ln, err := net.Listen("unix", socket)
	mustReply(t, err)
	srv := &http.Server{Handler: rpc.NewHandlerWithSSE(registry, w.Events), ConnContext: rpc.ConnContext}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return &replyTopicFixture{socket: socket, registry: registry, hub: hub,
		owner: client.New(socket, client.UnixDialer, 10*time.Second)}
}

// openReplay appends prompt as a user turn and opens its reply stream.
func openReplay(t *testing.T, f *replyTopicFixture, prompt string) (threadID, streamID string) {
	t.Helper()
	var turn struct {
		ThreadID string `json:"thread_id"`
		TurnID   string `json:"turn_id"`
	}
	params := map[string]any{"role": "user", "privacy_mode": "restricted",
		"segments": []map[string]string{{"kind": "text", "content": prompt}}}
	mustReply(t, f.owner.Do(context.Background(), conversation.MethodAppendTurn, params, &turn))
	var opened struct {
		StreamID string `json:"stream_id"`
	}
	mustReply(t, f.owner.Do(context.Background(), "chat.reply_open",
		map[string]string{"thread_id": turn.ThreadID, "turn_id": turn.TurnID}, &opened))
	return turn.ThreadID, opened.StreamID
}

type threadView struct {
	Turns []struct {
		Turn     conversation.Turn      `json:"turn"`
		Segments []conversation.Segment `json:"segments"`
	} `json:"turns"`
}

// readThread reads threadID through a fresh client: a reconnect.
func readThread(t *testing.T, f *replyTopicFixture, threadID string) threadView {
	t.Helper()
	var got threadView
	fresh := client.New(f.socket, client.UnixDialer, 10*time.Second)
	mustReply(t, fresh.Do(context.Background(), conversation.MethodGetThread,
		map[string]string{"thread_id": threadID}, &got))
	return got
}

func TestP1ChatReplyRoundTrip(t *testing.T) {
	ex := loadReplayExchange(t)
	f := newReplayFixture(t, replayDoor{parts: splitReply(ex.Reply)})
	threadID, id := openReplay(t, f, ex.Prompt)
	var res conversation.ReplyResult
	mustReply(t, f.owner.Do(context.Background(), conversation.MethodReply, map[string]string{"stream_id": id}, &res))
	if res.ThreadID != threadID || res.AssistantTurnID == "" || res.Replayed {
		t.Fatalf("chat.reply = %+v, want a fresh assistant turn on %s", res, threadID)
	}
	got := readThread(t, f, threadID)
	if len(got.Turns) != 2 {
		t.Fatalf("thread holds %d turns, want the user and the assistant turn", len(got.Turns))
	}
	user, asst := got.Turns[0], got.Turns[1]
	if user.Turn.Role != conversation.RoleUser || asst.Turn.Role != conversation.RoleAssistant ||
		asst.Turn.ID != res.AssistantTurnID || user.Turn.ThreadID != threadID || asst.Turn.ThreadID != threadID {
		t.Fatalf("turns %+v / %+v do not share thread %s", user.Turn, asst.Turn, threadID)
	}
	var text strings.Builder
	for _, s := range asst.Segments {
		text.WriteString(s.Content)
	}
	if text.String() != ex.Reply {
		t.Fatalf("durable assistant text %q, want the recorded reply %q", text.String(), ex.Reply)
	}
}

func TestP1ChatReplyCancelAfterFirstToken(t *testing.T) {
	ex := loadReplayExchange(t)
	parts := splitReply(ex.Reply)
	f := newReplayFixture(t, replayDoor{parts: parts, hold: true})
	threadID, id := openReplay(t, f, ex.Prompt)
	resp := f.get(t, streamPath("stream_id", id), "", "owner-cli")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var res conversation.ReplyResult
	replied := make(chan error, 1)
	go func() { replied <- f.owner.Do(ctx, conversation.MethodReply, map[string]string{"stream_id": id}, &res) }()
	frames := make(chan []conversation.Frame, 1)
	go func() { frames <- readFrames(t, resp) }()
	waitFirstToken(t, f, id)
	cancel()
	err := <-replied
	var ce *cascade.Error
	if !errors.As(err, &ce) || ce.Kind != cascade.KindCanceled || ce.Msg != "client: "+conversation.MethodReply+" canceled" {
		t.Fatalf("chat.reply after cancel = %v, want the typed client cancellation", err)
	}
	if res != (conversation.ReplyResult{}) {
		t.Fatalf("a cancelled reply reported success: %+v", res)
	}
	got := <-frames
	if len(got) == 0 || got[0].Delta != parts[0] {
		t.Fatalf("frames %+v, want the first token first", got)
	}
	if last := got[len(got)-1]; last.Type != conversation.FrameError || last.Kind != cascade.KindCanceled.String() {
		t.Fatalf("terminal frame %+v, want an error frame naming canceled", last)
	}
	if th := readThread(t, f, threadID); len(th.Turns) != 1 || th.Turns[0].Turn.Role != conversation.RoleUser {
		t.Fatalf("thread holds %d turns after cancel, want only the user turn", len(th.Turns))
	}
}

// waitFirstToken waits until the hub has buffered the first delta of id.
func waitFirstToken(t *testing.T, f *replyTopicFixture, id string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ch, err := f.hub.Subscribe(ctx, id, principal.Caller{OwnerCLI: true})
	mustReply(t, err)
	if fr, ok := <-ch; !ok || fr.Type != conversation.FrameDelta {
		t.Fatalf("first frame %+v (open %v), want a delta", fr, ok)
	}
}
