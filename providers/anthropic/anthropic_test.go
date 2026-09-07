// Purpose: the anthropic driver's unit suite - Chat/Embed/Count/
//   Capabilities/Stream against a recording HTTPDoer fake, replaying the
//   vendor wire shapes transcribed in testdata/README.md (Art.2.2), plus
//   FuzzAnthropicWireDecode. No "net"/"net/http" import (Art.7.2).
// SPORT: placeholder: providers/anthropic driver (ADD) - see anthropic.go.

package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

var _ provider.ModelProvider = (*Driver)(nil)

// fakeResp is one queued response (or transport error) fakeDoer returns.
type fakeResp struct {
	status int
	body   string
	err    error
}

// fakeDoer is a recording HTTPDoer fake: it never opens a socket, and every
// call is recorded so a test can assert what was actually sent.
type fakeDoer struct {
	queue []fakeResp
	reqs  []HTTPRequest
}

func (f *fakeDoer) Do(ctx context.Context, req HTTPRequest) (HTTPResponse, error) {
	f.reqs = append(f.reqs, req)
	if err := ctx.Err(); err != nil {
		return HTTPResponse{}, err
	}
	if len(f.queue) == 0 {
		return HTTPResponse{}, errors.New("fakeDoer: no queued response")
	}
	r := f.queue[0]
	f.queue = f.queue[1:]
	if r.err != nil {
		return HTTPResponse{}, r.err
	}
	return HTTPResponse{Status: r.status, Body: io.NopCloser(strings.NewReader(r.body))}, nil
}

func keyAuthDriver(t *testing.T, doer *fakeDoer) *Driver {
	t.Helper()
	d, err := New(Config{
		Doer:  doer,
		Clock: fixedClock(time.Unix(1000, 0)),
		Auth:  AuthConfig{Mode: AuthModeKey, KeyRef: "k", Resolver: fakeResolver{values: map[string]string{"k": "sk-test"}}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return d
}

const recordedChatResponse = `{"id":"msg_01","type":"message","role":"assistant","content":[{"type":"text","text":"hi there"}],"model":"claude-3-5-sonnet-20241022","stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":3}}`

// TestAnthropicDriverRecordedFixtures drives Chat and Count against the
// transcribed vendor wire shapes (testdata/README.md's provenance note) and
// asserts every documented error status maps onto the frozen taxonomy.
// TestAnthropicDriverRecordedFixtures drives Chat and Count against the
// transcribed vendor wire shapes and asserts every documented error status
// maps onto the frozen taxonomy. Split into three helpers to stay under
// the 50-line function cap.
func TestAnthropicDriverRecordedFixtures(t *testing.T) {
	t.Run("chat happy path", testRecordedChatHappyPath)
	t.Run("count happy path", testRecordedCountHappyPath)
	testRecordedStatusMapping(t)
}

func testRecordedChatHappyPath(t *testing.T) {
	doer := &fakeDoer{queue: []fakeResp{{status: 200, body: recordedChatResponse}}}
	d := keyAuthDriver(t, doer)
	resp, err := d.Chat(context.Background(), provider.ChatRequest{
		Messages: []provider.ChatMessage{{Role: "system", Content: "be terse"}, {Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Message.Content != "hi there" || resp.FinishReason != "stop" || resp.Usage.OutputTokens != 3 {
		t.Fatalf("resp = %+v", resp)
	}
	if len(doer.reqs) != 1 || doer.reqs[0].Headers["x-api-key"] != "sk-test" {
		t.Fatalf("request headers = %+v", doer.reqs)
	}
	var sent wireChatRequest
	if err := json.Unmarshal(doer.reqs[0].Body, &sent); err != nil {
		t.Fatalf("decoding sent body: %v", err)
	}
	if sent.System != "be terse" || len(sent.Messages) != 1 {
		t.Fatalf("sent = %+v", sent)
	}
}

func testRecordedCountHappyPath(t *testing.T) {
	doer := &fakeDoer{queue: []fakeResp{{status: 200, body: `{"input_tokens":42}`}}}
	d := keyAuthDriver(t, doer)
	resp, err := d.Count(context.Background(), provider.CountRequest{Text: "hello world"})
	if err != nil || resp.Tokens != 42 {
		t.Fatalf("Count: resp=%+v err=%v", resp, err)
	}
}

func testRecordedStatusMapping(t *testing.T) {
	statusCases := []struct {
		status int
		body   string
		want   cascade.Kind
	}{
		{400, `{"type":"error","error":{"type":"invalid_request_error","message":"bad"}}`, cascade.KindInvalidInput},
		{403, `{"type":"error","error":{"type":"permission_error","message":"denied"}}`, cascade.KindPermissionDenied},
		{404, `{"type":"error","error":{"type":"not_found_error","message":"missing"}}`, cascade.KindNotFound},
		{413, `{"type":"error","error":{"type":"request_too_large","message":"big"}}`, cascade.KindInvalidInput},
		{429, `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`, cascade.KindQuotaExhausted},
		{500, `{"type":"error","error":{"type":"api_error","message":"oops"}}`, cascade.KindUnavailable},
		{529, `{"type":"error","error":{"type":"overloaded_error","message":"busy"}}`, cascade.KindUnavailable},
	}
	for _, tc := range statusCases {
		t.Run("status "+tc.body[:20], func(t *testing.T) {
			doer := &fakeDoer{queue: []fakeResp{{status: tc.status, body: tc.body}}}
			d := keyAuthDriver(t, doer)
			req := provider.ChatRequest{Messages: []provider.ChatMessage{{Role: "user", Content: "hi"}}}
			_, err := d.Chat(context.Background(), req)
			kind, ok := cascade.KindOf(err)
			if !ok || kind != tc.want {
				t.Fatalf("status %d: kind = %v, ok=%v, want %v", tc.status, kind, ok, tc.want)
			}
		})
	}
}

func TestChatRejectsUnknownRole(t *testing.T) {
	d := keyAuthDriver(t, &fakeDoer{})
	_, err := d.Chat(context.Background(), provider.ChatRequest{Messages: []provider.ChatMessage{{Role: "tool", Content: "x"}}})
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
		t.Fatalf("kind = %v, ok=%v, want KindInvalidInput", kind, ok)
	}
}

func TestChatRequiresATurn(t *testing.T) {
	d := keyAuthDriver(t, &fakeDoer{})
	_, err := d.Chat(context.Background(), provider.ChatRequest{Messages: []provider.ChatMessage{{Role: "system", Content: "x"}}})
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
		t.Fatalf("kind = %v, ok=%v, want KindInvalidInput", kind, ok)
	}
}

func TestEmbedReturnsUnsupported(t *testing.T) {
	d := keyAuthDriver(t, &fakeDoer{})
	_, err := d.Embed(context.Background(), provider.ModelEmbedRequest{Inputs: []string{"x"}})
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindUnsupported {
		t.Fatalf("kind = %v, ok=%v, want KindUnsupported", kind, ok)
	}
}

func TestCapabilitiesForbidsCredentialSharing(t *testing.T) {
	d := keyAuthDriver(t, &fakeDoer{})
	caps, err := d.Capabilities(context.Background(), "")
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	if err := caps.CompliancePosture.Validate(); err != nil {
		t.Fatalf("compliance posture invalid: %v", err)
	}
	if caps.CompliancePosture.CredentialSharing != provider.CredentialSharingForbidden {
		t.Fatalf("credential_sharing = %q", caps.CompliancePosture.CredentialSharing)
	}
	if caps.Vision != provider.CapabilitySupported || caps.ToolUse != provider.CapabilitySupported {
		t.Fatalf("caps = %+v", caps)
	}
}

func TestChatCanceledContext(t *testing.T) {
	d := keyAuthDriver(t, &fakeDoer{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := d.Chat(ctx, provider.ChatRequest{Messages: []provider.ChatMessage{{Role: "user", Content: "x"}}})
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindCanceled {
		t.Fatalf("kind = %v, ok=%v, want KindCanceled", kind, ok)
	}
}

const recordedStreamSSE = "event: message_start\ndata: {\"type\":\"message_start\"}\n\n" +
	"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"Hi\"}}\n\n" +
	"event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"input_tokens\":4,\"output_tokens\":1}}\n\n" +
	"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

func TestStreamHappyPath(t *testing.T) {
	doer := &fakeDoer{queue: []fakeResp{{status: 200, body: recordedStreamSSE}}}
	d := keyAuthDriver(t, doer)
	var deltas []string
	sawDone := false
	req := provider.ChatRequest{Messages: []provider.ChatMessage{{Role: "user", Content: "hi"}}}
	err := d.Stream(context.Background(), req, func(ev provider.StreamEvent) error {
		switch ev.Kind {
		case provider.StreamEventDelta:
			deltas = append(deltas, ev.Delta)
		case provider.StreamEventDone:
			sawDone = true
		case provider.StreamEventUnknown, provider.StreamEventToolCall, provider.StreamEventUsage, provider.StreamEventError:
			// Not asserted by this happy-path test.
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if !sawDone || len(deltas) != 1 || deltas[0] != "Hi" {
		t.Fatalf("deltas=%v done=%v", deltas, sawDone)
	}
}

func TestStreamMalformedChunkReportsIntegrityError(t *testing.T) {
	body := "event: content_block_delta\ndata: not-json\n\n"
	doer := &fakeDoer{queue: []fakeResp{{status: 200, body: body}}}
	d := keyAuthDriver(t, doer)
	var sawErrorEvent bool
	req := provider.ChatRequest{Messages: []provider.ChatMessage{{Role: "user", Content: "hi"}}}
	err := d.Stream(context.Background(), req, func(ev provider.StreamEvent) error {
		sawErrorEvent = sawErrorEvent || ev.Kind == provider.StreamEventError
		return nil
	})
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindIntegrity {
		t.Fatalf("kind = %v, ok=%v, want KindIntegrity", kind, ok)
	}
	if !sawErrorEvent {
		t.Fatal("sink never received the terminal error event")
	}
}

func TestStreamNonOKStatus(t *testing.T) {
	doer := &fakeDoer{queue: []fakeResp{{status: 500, body: `{"type":"error","error":{"type":"api_error","message":"boom"}}`}}}
	d := keyAuthDriver(t, doer)
	req := provider.ChatRequest{Messages: []provider.ChatMessage{{Role: "user", Content: "hi"}}}
	err := d.Stream(context.Background(), req, func(provider.StreamEvent) error { return nil })
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindUnavailable {
		t.Fatalf("kind = %v, ok=%v, want KindUnavailable", kind, ok)
	}
}

func TestStreamSinkErrorAborts(t *testing.T) {
	doer := &fakeDoer{queue: []fakeResp{{status: 200, body: recordedStreamSSE}}}
	d := keyAuthDriver(t, doer)
	sinkErr := errors.New("sink refuses")
	req := provider.ChatRequest{Messages: []provider.ChatMessage{{Role: "user", Content: "hi"}}}
	err := d.Stream(context.Background(), req, func(provider.StreamEvent) error { return sinkErr })
	if !errors.Is(err, sinkErr) {
		t.Fatalf("err = %v, want sinkErr", err)
	}
}

// FuzzAnthropicWireDecode fuzzes the SSE decode path (scanSSEEvents +
// decodeSSEEvent) - untrusted network input by this repo's definition. It
// must never panic, however malformed or truncated the input. Go auto-loads
// this package's testdata/fuzz/FuzzAnthropicWireDecode/seed_response.json
// (native corpus encoding) as a seed on every run, including plain
// `go test`; the entries below are additional inline edge cases.
func FuzzAnthropicWireDecode(f *testing.F) {
	for _, seed := range [][]byte{
		nil, {}, []byte("event: content_block_delta\ndata: {\n\n"),
		[]byte("event: error\ndata: not-json\n\n"),
		[]byte("event: message_stop\ndata: {}"),
		[]byte(":\n\n"),
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, in []byte) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("decode panicked on %q: %v", in, r)
			}
		}()
		for _, ev := range scanSSEEvents(strings.NewReader(string(in))) {
			_, _, _ = decodeSSEEvent(ev)
		}
	})
}
