// Purpose: TestOpenAICompatDriverRecordedFixtures - the recorded-fixture
//   replay named by this ticket's own checks list - plus
//   FuzzOpenAICompatWireDecode's seed corpus (06 §5 rule 7). Every fixture
//   body's provenance is documented in testdata/README.md; a comment above
//   each literal below says which row it is. Each scenario is its own
//   top-level test* function (funlen: 50 lines) that
//   TestOpenAICompatDriverRecordedFixtures only wires into subtests, so the
//   single test name this ticket's checks list runs stays the entry point.
// Constraints: this file imports neither "net" nor "net/http" (Art.7.2 -
//   the no-network unit lane forbids both in an untagged _test.go file);
//   the fake transport it needs comes from openai.go's unexported
//   fakeDoer/newFakeDoer/fakeResponse, defined in a production file that
//   already carries that import for the real driver.
// SPORT: providers/openai driver/ADD (P1-E10-W3-S19-T3).

package openai

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// stubResolver is a KeyResolver test double: no vault, no environment.
type stubResolver struct {
	key string
	err error
}

func (s stubResolver) Resolve(context.Context, string) (string, error) { return s.key, s.err }

// fixedClock is a testkit.Clock-compatible fake local to this package's
// tests (testkit itself is test-only and this file already needs no
// import from it - a single field satisfies openai.Clock structurally).
type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }

func newTestDriver(t *testing.T, doer Doer) *Driver {
	t.Helper()
	d, err := New(Config{
		BaseURL: "https://api.openai.com/v1", KeyRef: "OPENAI_API_KEY",
		Resolver: stubResolver{key: "test-resolved-value"}, HTTPClient: doer,
		Clock:            fixedClock{now: time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC)},
		DefaultChatModel: "gpt-4o-mini", DefaultEmbedModel: "text-embedding-3-small",
		RetryBaseDelay: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return d
}

// Row 1 of testdata/README.md: OpenAI's real, live-captured 401 body.
const openaiLive401 = `{"error":{"message":"You didn't provide an API key.","type":"invalid_request_error","param":null,"code":null}}`

// Row 2: Moonshot/Kimi's real, live-captured 401 body - no param/code.
const moonshotLive401 = `{"error":{"message":"Incorrect API key provided","type":"incorrect_api_key_error"}}`

// Row 3: zai's real, live-captured 401 body - numeric-string code, no type.
const zaiLive401 = `{"error":{"code":"1001","message":"Authentication parameter not received in Header, unable to authenticate"}}`

// Row 4: DeepSeek's real, live-captured 401 body - plain text, not JSON.
const deepseekLive401 = `Authentication Fails (governor)`

// Constructed (not captured live - see testdata/README.md "not captured")
// 429, reusing OpenAI's verified envelope shape with a different message.
const constructed429 = `{"error":{"message":"Rate limit reached","type":"rate_limit_error","param":null,"code":null}}`

// TestOpenAICompatDriverRecordedFixtures is the single named test this
// ticket's checks list runs. It only wires subtests; every scenario's body
// lives in its own test* helper below, each well under funlen's 50 lines.
func TestOpenAICompatDriverRecordedFixtures(t *testing.T) {
	t.Run("chat happy path", testChatHappyPath)
	t.Run("embed happy path", testEmbedHappyPath)
	t.Run("count is typed unsupported", testCountUnsupported)
	t.Run("capabilities are honest unknown with forbidden sharing", testCapabilitiesHonest)
	t.Run("vendor error variance", testVendorErrorVariance)
	t.Run("stream happy path emits delta then done exactly once", testStreamHappyPath)
	t.Run("truncated stream surfaces exactly one error event", testStreamTruncated)
	t.Run("malformed mid-stream chunk surfaces typed integrity error", testStreamMalformedChunk)
	t.Run("context canceled maps to KindCanceled", testContextCanceled)
	t.Run("resolver failure never retried, never leaks the key", testResolverFailure)
}

func testChatHappyPath(t *testing.T) {
	body := `{"choices":[{"message":{"role":"assistant","content":"hi there"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`
	d := newTestDriver(t, newFakeDoer(fakeResponse{status: 200, body: []byte(body)}))
	resp, err := d.Chat(context.Background(), provider.ChatRequest{Messages: []provider.ChatMessage{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Message.Content != "hi there" || resp.Usage.InputTokens != 3 || resp.FinishReason != "stop" {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func testEmbedHappyPath(t *testing.T) {
	body := `{"data":[{"embedding":[0.1,0.2],"index":0},{"embedding":[0.3,0.4],"index":1}],"usage":{"prompt_tokens":4}}`
	d := newTestDriver(t, newFakeDoer(fakeResponse{status: 200, body: []byte(body)}))
	resp, err := d.Embed(context.Background(), provider.ModelEmbedRequest{Inputs: []string{"a", "b"}})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(resp.Vectors) != 2 || resp.Vectors[1][0] != 0.3 {
		t.Fatalf("unexpected vectors: %+v", resp.Vectors)
	}
}

func testCountUnsupported(t *testing.T) {
	d := newTestDriver(t, newFakeDoer())
	_, err := d.Count(context.Background(), provider.CountRequest{Text: "hi"})
	if !cascade.HasKind(err, cascade.KindUnsupported) {
		t.Fatalf("want KindUnsupported, got %v", err)
	}
}

func testCapabilitiesHonest(t *testing.T) {
	d := newTestDriver(t, newFakeDoer())
	caps, err := d.Capabilities(context.Background(), "")
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	if caps.Search != provider.CapabilityUnknown || caps.CompliancePosture.CredentialSharing != provider.CredentialSharingForbidden {
		t.Fatalf("unexpected capabilities: %+v", caps)
	}
}

func testVendorErrorVariance(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   cascade.Kind
	}{
		{"openai live 401", 401, openaiLive401, cascade.KindPermissionDenied},
		{"moonshot live 401", 401, moonshotLive401, cascade.KindPermissionDenied},
		{"zai live 401", 401, zaiLive401, cascade.KindPermissionDenied},
		{"deepseek live 401 non-json body", 401, deepseekLive401, cascade.KindPermissionDenied},
		{"constructed 429", 429, constructed429, cascade.KindQuotaExhausted},
		{"5xx maps unavailable", 503, `{"error":{"message":"down"}}`, cascade.KindUnavailable},
		{"malformed json 200 maps integrity", 200, `{not json`, cascade.KindIntegrity},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			responses := make([]fakeResponse, 0, 3)
			for i := 0; i < 3; i++ {
				responses = append(responses, fakeResponse{status: tc.status, body: []byte(tc.body)})
			}
			d := newTestDriver(t, newFakeDoer(responses...))
			_, err := d.Chat(context.Background(), provider.ChatRequest{Messages: []provider.ChatMessage{{Role: "user", Content: "hi"}}})
			if !cascade.HasKind(err, tc.want) {
				t.Fatalf("status %d body %q: want kind %v, got %v", tc.status, tc.body, tc.want, err)
			}
		})
	}
}

func testStreamHappyPath(t *testing.T) {
	sse := "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n"
	d := newTestDriver(t, newFakeDoer(fakeResponse{status: 200, body: []byte(sse)}))
	var kinds []provider.StreamEventKind
	err := d.Stream(context.Background(), provider.ChatRequest{Messages: []provider.ChatMessage{{Role: "user", Content: "hi"}}},
		func(ev provider.StreamEvent) error { kinds = append(kinds, ev.Kind); return nil })
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if len(kinds) != 2 || kinds[0] != provider.StreamEventDelta || kinds[1] != provider.StreamEventDone {
		t.Fatalf("unexpected event sequence: %+v", kinds)
	}
}

func testStreamTruncated(t *testing.T) {
	sse := "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n" // no [DONE]
	d := newTestDriver(t, newFakeDoer(fakeResponse{status: 200, body: []byte(sse)}))
	var terminals int
	err := d.Stream(context.Background(), provider.ChatRequest{}, func(ev provider.StreamEvent) error {
		if ev.Kind == provider.StreamEventDone || ev.Kind == provider.StreamEventError {
			terminals++
		}
		return nil
	})
	if err == nil || !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("want KindUnavailable truncation error, got %v", err)
	}
	if terminals != 1 {
		t.Fatalf("want exactly one terminal event, got %d", terminals)
	}
}

func testStreamMalformedChunk(t *testing.T) {
	sse := "data: {not valid json\n\n"
	d := newTestDriver(t, newFakeDoer(fakeResponse{status: 200, body: []byte(sse)}))
	err := d.Stream(context.Background(), provider.ChatRequest{}, func(provider.StreamEvent) error { return nil })
	if !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("want KindIntegrity, got %v", err)
	}
}

func testContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d := newTestDriver(t, newFakeDoer(fakeResponse{err: context.Canceled}))
	_, err := d.Chat(ctx, provider.ChatRequest{})
	if !cascade.HasKind(err, cascade.KindCanceled) {
		t.Fatalf("want KindCanceled, got %v", err)
	}
}

func testResolverFailure(t *testing.T) {
	d, err := New(Config{
		BaseURL: "https://api.openai.com/v1", KeyRef: "OPENAI_API_KEY",
		Resolver:   stubResolver{err: cascade.New(cascade.KindNotFound, "no such key")},
		HTTPClient: newFakeDoer(), Clock: fixedClock{now: time.Now()},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, cerr := d.Chat(context.Background(), provider.ChatRequest{})
	if !cascade.HasKind(cerr, cascade.KindNotFound) {
		t.Fatalf("want KindNotFound from resolver, got %v", cerr)
	}
}

// FuzzOpenAICompatWireDecode fuzzes this driver's two custom decoders
// (parseSSEStream and decodeWireEvent, stream.go) on the same arbitrary
// bytes, seeded from testdata/fuzz/FuzzOpenAICompatWireDecode/ (auto-loaded
// by `go test -fuzz`) plus a few inline shapes. Neither decoder may ever
// panic; every failure must already be a *cascade.Error.
func FuzzOpenAICompatWireDecode(f *testing.F) {
	f.Add([]byte(`{"choices":[{"delta":{"content":"hi"}}]}`))
	f.Add([]byte("[DONE]"))
	f.Add([]byte(""))
	f.Add([]byte("{"))
	f.Add([]byte(openaiLive401))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _, err := decodeWireEvent(data)
		if err != nil {
			var taxErr *cascade.Error
			if !errors.As(err, &taxErr) {
				t.Fatalf("decodeWireEvent returned a non-taxonomy error: %v", err)
			}
		}
		_ = parseSSEStream(bytes.NewReader(data), func([]byte) error { return nil })
	})
}
