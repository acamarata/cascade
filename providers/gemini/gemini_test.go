// Purpose: the gemini driver's unit suite; replays testdata/README.md's
// transcribed wire shapes (Art.2.2) against a recording HTTPDoer fake.
// SPORT: placeholder: providers/gemini driver (ADD) - see gemini.go.

package gemini

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

type fakeResp struct { // one queued response fakeDoer returns
	status int
	body   string
	err    error
}

// fakeDoer is a recording HTTPDoer fake: no socket, every call recorded.
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
		Auth:  AuthConfig{Mode: AuthModeKey, KeyRef: "k", Resolver: fakeResolver{values: map[string]string{"k": "gk-test"}}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return d
}

const recordedChatResponse = `{"candidates":[{"content":{"parts":[{"text":"hi there"}],"role":"model"},"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":3,"totalTokenCount":8}}`

// TestGeminiDriverRecordedFixtures drives Chat/Embed/Count against the
// transcribed wire shapes and checks every status maps onto the taxonomy.
func TestGeminiDriverRecordedFixtures(t *testing.T) {
	t.Run("chat happy path", testRecordedChatHappyPath)
	t.Run("count happy path", testRecordedCountHappyPath)
	t.Run("embed happy path", testRecordedEmbedHappyPath)
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
	if len(doer.reqs) != 1 || doer.reqs[0].Headers["x-goog-api-key"] != "gk-test" {
		t.Fatalf("request headers = %+v", doer.reqs)
	}
	var sent wireGenerateRequest
	if err := json.Unmarshal(doer.reqs[0].Body, &sent); err != nil {
		t.Fatalf("decoding sent body: %v", err)
	}
	if sent.SystemInstruction == nil || sent.SystemInstruction.Parts[0].Text != "be terse" || len(sent.Contents) != 1 {
		t.Fatalf("sent = %+v", sent)
	}
}

func testRecordedCountHappyPath(t *testing.T) {
	doer := &fakeDoer{queue: []fakeResp{{status: 200, body: `{"totalTokens":42}`}}}
	d := keyAuthDriver(t, doer)
	resp, err := d.Count(context.Background(), provider.CountRequest{Text: "hello world"})
	if err != nil || resp.Tokens != 42 {
		t.Fatalf("Count: resp=%+v err=%v", resp, err)
	}
}

func testRecordedEmbedHappyPath(t *testing.T) {
	body := `{"embeddings":[{"values":[0.1,0.2]},{"values":[0.3,0.4]}]}`
	doer := &fakeDoer{queue: []fakeResp{{status: 200, body: body}}}
	d := keyAuthDriver(t, doer)
	resp, err := d.Embed(context.Background(), provider.ModelEmbedRequest{Inputs: []string{"a", "b"}})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(resp.Vectors) != 2 || resp.Vectors[0][0] != 0.1 || resp.Vectors[1][1] != 0.4 {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestEmbedErrorPaths(t *testing.T) {
	t.Run("empty inputs", func(t *testing.T) {
		d := keyAuthDriver(t, &fakeDoer{})
		_, err := d.Embed(context.Background(), provider.ModelEmbedRequest{})
		if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
			t.Fatalf("kind = %v, ok=%v, want KindInvalidInput", kind, ok)
		}
	})
	t.Run("vector count mismatch", func(t *testing.T) {
		doer := &fakeDoer{queue: []fakeResp{{status: 200, body: `{"embeddings":[{"values":[0.1]}]}`}}}
		d := keyAuthDriver(t, doer)
		_, err := d.Embed(context.Background(), provider.ModelEmbedRequest{Inputs: []string{"a", "b"}})
		if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindIntegrity {
			t.Fatalf("kind = %v, ok=%v, want KindIntegrity", kind, ok)
		}
	})
}

func testRecordedStatusMapping(t *testing.T) {
	statusCases := []struct {
		name   string
		status int
		body   string
		want   cascade.Kind
	}{
		{"bad request", 400, `{"error":{"code":400,"message":"bad","status":"INVALID_ARGUMENT"}}`, cascade.KindInvalidInput},
		{"unauthenticated", 401, `{"error":{"code":401,"message":"denied","status":"UNAUTHENTICATED"}}`, cascade.KindPermissionDenied},
		{"not found", 404, `{"error":{"code":404,"message":"missing","status":"NOT_FOUND"}}`, cascade.KindNotFound},
		{"quota", 429, `{"error":{"code":429,"message":"slow down","status":"RESOURCE_EXHAUSTED"}}`, cascade.KindQuotaExhausted},
		{"internal", 500, `{"error":{"code":500,"message":"oops","status":"INTERNAL"}}`, cascade.KindUnavailable},
		{"unavailable", 503, `{"error":{"code":503,"message":"busy","status":"UNAVAILABLE"}}`, cascade.KindUnavailable},
	}
	for _, tc := range statusCases {
		t.Run(tc.name, func(t *testing.T) {
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

func TestChatInputValidation(t *testing.T) {
	for name, msgs := range map[string][]provider.ChatMessage{
		"unknown role": {{Role: "tool", Content: "x"}},
		"no turns":     {{Role: "system", Content: "x"}},
	} {
		t.Run(name, func(t *testing.T) {
			d := keyAuthDriver(t, &fakeDoer{})
			_, err := d.Chat(context.Background(), provider.ChatRequest{Messages: msgs})
			if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
				t.Fatalf("kind = %v, ok=%v, want KindInvalidInput", kind, ok)
			}
		})
	}
}

const recordedStreamSSE = "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"Hi\"}],\"role\":\"model\"}}]}\n\n" +
	"data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"!\"}],\"role\":\"model\"},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":4,\"candidatesTokenCount\":2,\"totalTokenCount\":6}}\n\n"

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
	if !sawDone || len(deltas) != 2 || deltas[0] != "Hi" || deltas[1] != "!" {
		t.Fatalf("deltas=%v done=%v", deltas, sawDone)
	}
}

// Both malformed and truncated chunks are KindIntegrity, never a panic.
func TestStreamMalformedOrTruncatedChunkReportsIntegrityError(t *testing.T) {
	bodies := map[string]string{
		"malformed": "data: not-json\n\n",
		"truncated": "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"cut off",
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
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
		})
	}
}

func TestStreamMidStreamErrorChunkIsTypedAndTerminal(t *testing.T) {
	body := "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"Hi\"}]}}]}\n\n" +
		"data: {\"error\":{\"code\":429,\"message\":\"slow down\",\"status\":\"RESOURCE_EXHAUSTED\"}}\n\n" +
		"data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"never delivered\"}]}}]}\n\n"
	doer := &fakeDoer{queue: []fakeResp{{status: 200, body: body}}}
	d := keyAuthDriver(t, doer)
	var terminalCount int
	var deltas []string
	req := provider.ChatRequest{Messages: []provider.ChatMessage{{Role: "user", Content: "hi"}}}
	err := d.Stream(context.Background(), req, func(ev provider.StreamEvent) error {
		if ev.Kind == provider.StreamEventDone || ev.Kind == provider.StreamEventError {
			terminalCount++
		}
		if ev.Kind == provider.StreamEventDelta {
			deltas = append(deltas, ev.Delta)
		}
		return nil
	})
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindQuotaExhausted {
		t.Fatalf("kind = %v, ok=%v, want KindQuotaExhausted", kind, ok)
	}
	if terminalCount != 1 {
		t.Fatalf("terminalCount = %d, want exactly 1", terminalCount)
	}
	if len(deltas) != 1 || deltas[0] != "Hi" {
		t.Fatalf("deltas = %v, want exactly the pre-error delta", deltas)
	}
}

func TestStreamNonOKStatus(t *testing.T) {
	doer := &fakeDoer{queue: []fakeResp{{status: 500, body: `{"error":{"code":500,"message":"boom","status":"INTERNAL"}}`}}}
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

// FuzzGeminiWireDecode fuzzes the decode path; it must never panic.
func FuzzGeminiWireDecode(f *testing.F) {
	for _, seed := range [][]byte{nil, {}, []byte("data: {\n\n"), []byte("data: {\"error\":not-json}\n\n"), []byte("data: {}"), []byte(":\n\n")} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, in []byte) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("decode panicked on %q: %v", in, r)
			}
		}()
		for _, chunk := range scanGeminiSSE(strings.NewReader(string(in))) {
			_, _, _ = decodeGeminiChunk(chunk)
		}
	})
}
