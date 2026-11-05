// Purpose: error-branch coverage for cascadepa_wiring.go's chat.reply path
//
//	(OneShot, readReply, streamReply, forwardReply). A scripted rpcDoer
//	fails or blocks one method at a time; no sockets, sleeps or network.
//
// SPORT: internal/plugins:cascadepa-wiring (TEST) -- FIX-cascade-chat-client-wiring.
package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/conversation"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	pacmd "github.com/acamarata/cascade/plugins/cascade-pa/cmd"
)

var happyReplies = map[string]string{
	"chat.append_turn": `{"thread_id":"th","turn_id":"u"}`,
	"chat.reply_open":  `{"stream_id":"s1"}`,
	"chat.reply":       `{"thread_id":"th","turn_id":"u","assistant_turn_id":"a"}`,
	"chat.get_thread":  `{"turns":[{"turn":{"ID":"a","Role":"assistant"},"segments":[{"Kind":"text","Content":"ok"}]}]}`,
}

// scriptRPC answers every method with happyReplies unless hook intercepts.
type scriptRPC struct {
	hook func(ctx context.Context, method string) (handled bool, err error)
	raw  map[string]string
}

func (s *scriptRPC) Do(ctx context.Context, method string, _, out any) error {
	if s.hook != nil {
		if handled, err := s.hook(ctx, method); handled || err != nil {
			return err
		}
	}
	raw, ok := s.raw[method]
	if !ok {
		raw = happyReplies[method]
	}
	return json.Unmarshal([]byte(raw), out)
}

// subRPC adds the StreamTopic subscription on top of scriptRPC.
type subRPC struct {
	scriptRPC
	frames    chan client.Event
	streamErr error
}

func (s *subRPC) StreamTopic(context.Context, string, url.Values) (<-chan client.Event, func(), error) {
	return s.frames, func() {}, s.streamErr
}

var errBoom = errors.New("boom")

func failingPaths() (runtime.PathProvider, error) { return nil, errBoom }

func oneShot(d rpcDoer) (pacmd.OneShotResult, error) {
	return (&cascadePAClient{doer: d}).OneShot(context.Background(), pacmd.OneShotRequest{Prompt: "hi"})
}

func drainStream(ctx context.Context, c *cascadePAClient) error {
	tokens, errs := c.Stream(ctx, pacmd.OneShotRequest{Prompt: "hi"})
	for range tokens {
		continue
	}
	return <-errs
}

func TestCascadePAOneShotChatReplyError(t *testing.T) {
	d := &scriptRPC{hook: func(_ context.Context, m string) (bool, error) {
		if m == "chat.reply" {
			return true, errBoom
		}
		return false, nil
	}}
	if _, err := oneShot(d); !errors.Is(err, errBoom) {
		t.Fatalf("err = %v, want boom", err)
	}
}

func TestCascadePAOneShotGetThreadError(t *testing.T) {
	d := &scriptRPC{hook: func(_ context.Context, m string) (bool, error) {
		if m == "chat.get_thread" {
			return true, errBoom
		}
		return false, nil
	}}
	if _, err := oneShot(d); !errors.Is(err, errBoom) {
		t.Fatalf("err = %v, want boom", err)
	}
}

func TestReadReplyContextCanceledBeforeRead(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := readReply(ctx, &scriptRPC{}, conversation.ReplyResult{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context canceled", err)
	}
}

func TestReadReplyContextCanceledAfterRead(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	d := &scriptRPC{hook: func(_ context.Context, m string) (bool, error) {
		if m == "chat.get_thread" {
			cancel()
		}
		return false, nil
	}}
	if _, err := readReply(ctx, d, conversation.ReplyResult{ThreadID: "th", AssistantTurnID: "a"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context canceled", err)
	}
}

func TestReadReplyMissingAssistantTurn(t *testing.T) {
	d := &scriptRPC{raw: map[string]string{"chat.get_thread": `{"turns":[` +
		`{"turn":{"ID":"a","Role":"user"},"segments":[]},` +
		`{"turn":{"ID":"other","Role":"assistant"},"segments":[]}]}`}}
	_, err := readReply(context.Background(), d, conversation.ReplyResult{ThreadID: "th", AssistantTurnID: "a"})
	if !cascade.HasKind(err, cascade.KindIntegrity) || !strings.Contains(err.Error(), "no durable assistant turn") {
		t.Fatalf("err = %v, want integrity: no durable assistant turn", err)
	}
}

func TestCascadePAStreamSocketPathFailure(t *testing.T) {
	c := newCascadePAClient(client.UnixDialer, 0, failingPaths)
	err := drainStream(context.Background(), c)
	if !cascade.HasKind(err, cascade.KindUnavailable) || !strings.Contains(err.Error(), "resolve daemon socket path") {
		t.Fatalf("err = %v, want unavailable: resolve daemon socket path", err)
	}
}

func TestCascadePAStreamWithoutSubscriber(t *testing.T) {
	err := drainStream(context.Background(), &cascadePAClient{doer: &scriptRPC{}})
	if !cascade.HasKind(err, cascade.KindInternal) || !strings.Contains(err.Error(), "subscription unavailable") {
		t.Fatalf("err = %v, want internal: subscription unavailable", err)
	}
}

func TestCascadePAStreamSubscribeError(t *testing.T) {
	err := drainStream(context.Background(), &cascadePAClient{doer: &subRPC{streamErr: errBoom}})
	if !errors.Is(err, errBoom) {
		t.Fatalf("err = %v, want boom", err)
	}
}

func TestCascadePAStreamErrorFrameIsUnavailable(t *testing.T) {
	d := &subRPC{frames: make(chan client.Event, 1)}
	d.frames <- client.Event{Data: `{"type":"error","kind":"quota","thread_id":"th"}`}
	d.hook = func(ctx context.Context, m string) (bool, error) {
		if m == "chat.reply" {
			<-ctx.Done()
			return true, ctx.Err()
		}
		return false, nil
	}
	err := drainStream(context.Background(), &cascadePAClient{doer: d})
	if !cascade.HasKind(err, cascade.KindUnavailable) || !strings.Contains(err.Error(), "reply failed: quota") {
		t.Fatalf("err = %v, want unavailable: reply failed: quota", err)
	}
}
