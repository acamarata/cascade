// Purpose: the gemini driver's unit suite; replays testdata/README.md's
// transcribed wire shapes (Art.2.2) against a recording HTTPDoer fake.
// SPORT: placeholder: providers/gemini driver (ADD) - see gemini.go.

package gemini

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
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

// wireKind maps the fixture's want_kind wire string onto its cascade.Kind.
var wireKind = map[string]cascade.Kind{
	"capability-denied": cascade.KindCapabilityDenied, "unavailable": cascade.KindUnavailable,
}

func testRecordedStatusMapping(t *testing.T) {
	type sc struct {
		name   string
		status int
		body   string
		want   cascade.Kind
	}
	statusCases := []sc{
		{"bad request", 400, `{"error":{"code":400,"message":"bad","status":"INVALID_ARGUMENT"}}`, cascade.KindInvalidInput},
		{"unauthenticated", 401, `{"error":{"code":401,"message":"denied","status":"UNAUTHENTICATED"}}`, cascade.KindPermissionDenied},
		{"not found", 404, `{"error":{"code":404,"message":"missing","status":"NOT_FOUND"}}`, cascade.KindNotFound},
		{"quota", 429, `{"error":{"code":429,"message":"slow down","status":"RESOURCE_EXHAUSTED"}}`, cascade.KindQuotaExhausted},
		{"internal", 500, `{"error":{"code":500,"message":"oops","status":"INTERNAL"}}`, cascade.KindUnavailable},
	}
	// The 403/5xx rows come from the provenance-stamped fixture (Art.2):
	// exercised from a stated corpus, never a self-authored dialect.
	data, err := os.ReadFile("testdata/error_403_5xx_recorded.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var doc struct {
		Cases []struct {
			Name     string `json:"name"`
			Body     string `json:"body"`
			WantKind string `json:"want_kind"`
			Status   int    `json:"status"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	for _, c := range doc.Cases {
		statusCases = append(statusCases, sc{c.Name, c.Status, c.Body, wireKind[c.WantKind]})
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
