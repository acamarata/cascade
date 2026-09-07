// Purpose: the ollama driver's unit suite against a recording HTTPDoer fake, replaying wire shapes from testdata/README.md, plus FuzzOllamaWireDecode. No "net"/"net/http" import (Art.7.2).

package ollama

import (
	"bufio"
	"bytes"
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

type fixedClock time.Time

func (c fixedClock) Now() time.Time { return time.Time(c) }

type fakeResolver struct{ values map[string]string }

func (r fakeResolver) Resolve(_ context.Context, ref string) (string, error) {
	if v, ok := r.values[ref]; ok {
		return v, nil
	}
	return "", cascade.Newf(cascade.KindNotFound, "fakeResolver: no value for %q", ref)
}

type fakeResp struct {
	status int
	body   string
	err    error
}
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
func driverWith(t *testing.T, resps ...fakeResp) (*Driver, *fakeDoer) {
	t.Helper()
	doer := &fakeDoer{queue: resps}
	d, err := New(Config{Doer: doer, Clock: fixedClock(time.Unix(1000, 0))})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return d, doer
}
func td(t *testing.T, resps ...fakeResp) *Driver { d, _ := driverWith(t, resps...); return d }
func kindOf(t *testing.T, err error) cascade.Kind {
	t.Helper()
	kind, ok := cascade.KindOf(err)
	if !ok {
		t.Fatalf("err = %v, not a cascade.Error", err)
	}
	return kind
}
func chatCtx(ctx context.Context, d *Driver) (provider.ChatResponse, error) {
	return d.Chat(ctx, provider.ChatRequest{Messages: []provider.ChatMessage{{Role: "user", Content: "x"}}})
}
func chatOne(d *Driver) (provider.ChatResponse, error) { return chatCtx(context.Background(), d) }
func streamOne(t *testing.T, body string, sink provider.StreamSink) error {
	req := provider.ChatRequest{Messages: []provider.ChatMessage{{Role: "user", Content: "hi"}}}
	return td(t, fakeResp{status: 200, body: body}).Stream(context.Background(), req, sink)
}

const recordedChatResponse = `{"model":"llama3","created_at":"2026-09-06T00:00:00Z","message":{"role":"assistant","content":"hi there"},"done":true,"done_reason":"stop","prompt_eval_count":5,"eval_count":3}`
const recordedStreamNDJSON = `{"model":"llama3","message":{"role":"assistant","content":"Hi"},"done":false}
{"model":"llama3","message":{"role":"assistant","content":""},"done":true,"done_reason":"stop","prompt_eval_count":4,"eval_count":1}
`

// TestOllamaDriverRecordedFixtures drives every verb's happy path against the transcribed vendor wire shapes.
func TestOllamaDriverRecordedFixtures(t *testing.T) {
	t.Run("chat", testRecordedChat)
	t.Run("embed capabilities bearer-token", testRecordedEmbedCapsToken)
	t.Run("stream happy path", testRecordedStreamHappy)
}
func testRecordedChat(t *testing.T) {
	d, doer := driverWith(t, fakeResp{status: 200, body: recordedChatResponse})
	resp, err := d.Chat(context.Background(), provider.ChatRequest{
		Messages: []provider.ChatMessage{{Role: "system", Content: "be terse"}, {Role: "user", Content: "hello"}},
	})
	if err != nil || resp.Message.Content != "hi there" || resp.FinishReason != "stop" || resp.Usage.OutputTokens != 3 {
		t.Fatalf("resp = %+v, err=%v", resp, err)
	}
	var sent wireChatRequest
	jerr := json.Unmarshal(doer.reqs[0].Body, &sent)
	if jerr != nil || len(sent.Messages) != 2 || doer.reqs[0].URL != defaultBaseURL+"/api/chat" {
		t.Fatalf("sent = %+v, url=%s, err=%v", sent, doer.reqs[0].URL, jerr)
	}
}
func testRecordedEmbedCapsToken(t *testing.T) {
	resp, err := td(t, fakeResp{status: 200, body: `{"embeddings":[[0.1,0.2],[0.3,0.4]],"prompt_eval_count":7}`}).
		Embed(context.Background(), provider.ModelEmbedRequest{Model: "m", Inputs: []string{"a", "b"}})
	if err != nil || len(resp.Vectors) != 2 || resp.Vectors[0][0] != 0.1 || resp.Usage.InputTokens != 7 {
		t.Fatalf("Embed: resp = %+v, err=%v", resp, err)
	}
	caps, err := td(t, fakeResp{status: 200, body: `{"models":[{"name":"llama3"}]}`}).Capabilities(context.Background(), "llama3")
	if err != nil || caps.CompliancePosture.Validate() != nil {
		t.Fatalf("caps = %+v, err=%v", caps, err)
	}
	if caps.CompliancePosture.CredentialSharing != provider.CredentialSharingForbidden ||
		caps.CompliancePosture.InteractiveEntitlement || caps.Search != provider.CapabilityUnknown {
		t.Fatalf("caps = %+v", caps)
	}
	tokenDoer := &fakeDoer{queue: []fakeResp{{status: 200, body: recordedChatResponse}}}
	tokenD, terr := New(Config{
		Doer: tokenDoer, Clock: fixedClock(time.Unix(0, 0)),
		TokenRef: "tok", Resolver: fakeResolver{values: map[string]string{"tok": "secret-value"}},
	})
	if terr != nil {
		t.Fatalf("New: %v", terr)
	}
	if _, cerr := chatOne(tokenD); cerr != nil || tokenDoer.reqs[0].Headers["authorization"] != "Bearer secret-value" {
		t.Fatalf("Chat err=%v, authorization=%q", cerr, tokenDoer.reqs[0].Headers["authorization"])
	}
}
func testRecordedStreamHappy(t *testing.T) {
	var deltas []string
	sawDone := false
	err := streamOne(t, recordedStreamNDJSON, func(ev provider.StreamEvent) error {
		switch ev.Kind {
		case provider.StreamEventDelta:
			deltas = append(deltas, ev.Delta)
		case provider.StreamEventDone:
			sawDone = true
		case provider.StreamEventUnknown, provider.StreamEventToolCall, provider.StreamEventUsage, provider.StreamEventError:
		}
		return nil
	})
	if err != nil || !sawDone || len(deltas) != 1 || deltas[0] != "Hi" {
		t.Fatalf("deltas=%v done=%v err=%v", deltas, sawDone, err)
	}
}

// TestOllamaDriverErrorPaths asserts every failure mode maps onto the frozen taxonomy (nothing-listening -> KindUnavailable naming the server; every streaming failure -> exactly one terminal event).
func TestOllamaDriverErrorPaths(t *testing.T) {
	t.Run("status mapping", testErrStatusMapping)
	t.Run("simple kind table", testErrSimpleKinds)
	t.Run("connection refused and token", testErrConnectionAndToken)
	t.Run("stream failure paths", testErrStreamFailures)
}
func testErrStatusMapping(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   cascade.Kind
	}{
		{400, `{"error":"bad request"}`, cascade.KindInvalidInput},
		{404, `{"error":"model 'ghost' not found, try pulling it first"}`, cascade.KindNotFound},
		{500, `{"error":"internal error"}`, cascade.KindUnavailable},
	} {
		if _, err := chatOne(td(t, fakeResp{status: tc.status, body: tc.body})); kindOf(t, err) != tc.want {
			t.Fatalf("status %d: kind = %v, want %v", tc.status, kindOf(t, err), tc.want)
		}
	}
}
func testErrConnectionAndToken(t *testing.T) {
	_, err := chatOne(td(t, fakeResp{err: errors.New("dial tcp 127.0.0.1:11434: connect: connection refused")}))
	if kindOf(t, err) != cascade.KindUnavailable || !strings.Contains(err.Error(), defaultBaseURL) || !strings.Contains(err.Error(), "running") {
		t.Fatalf("connection refused: kind = %v, err = %q, want KindUnavailable naming the server", kindOf(t, err), err)
	}
	tokenD, terr := New(Config{Doer: &fakeDoer{}, Clock: fixedClock(time.Unix(1000, 0)), TokenRef: "missing", Resolver: fakeResolver{}})
	if terr != nil {
		t.Fatalf("New: %v", terr)
	}
	if _, cerr := chatOne(tokenD); kindOf(t, cerr) != cascade.KindNotFound {
		t.Fatalf("unresolvable token: kind = %v, want KindNotFound", kindOf(t, cerr))
	}
}
func testErrSimpleKinds(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func() error
		want cascade.Kind
	}{
		{"canceled", func() error {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			_, err := chatCtx(ctx, td(t))
			return err
		}, cascade.KindCanceled},
		{"deadline", func() error {
			ctx, cancel := context.WithTimeout(context.Background(), 0)
			defer cancel()
			<-ctx.Done()
			_, err := chatCtx(ctx, td(t))
			return err
		}, cascade.KindTimeout},
		{"unknown model 404", func() error {
			_, err := td(t, fakeResp{status: 200, body: `{"models":[{"name":"other"}]}`}).Capabilities(context.Background(), "ghost")
			return err
		}, cascade.KindNotFound},
		{"embed empty inputs", func() error {
			_, err := td(t).Embed(context.Background(), provider.ModelEmbedRequest{Model: "m"})
			return err
		}, cascade.KindInvalidInput},
		{"embed mismatched vectors", func() error {
			doer := fakeResp{status: 200, body: `{"embeddings":[[0.1]],"prompt_eval_count":1}`}
			_, err := td(t, doer).Embed(context.Background(), provider.ModelEmbedRequest{Model: "m", Inputs: []string{"a", "b"}})
			return err
		}, cascade.KindIntegrity},
		{"chat unknown role", func() error {
			_, err := td(t).Chat(context.Background(), provider.ChatRequest{Messages: []provider.ChatMessage{{Role: "narrator", Content: "x"}}})
			return err
		}, cascade.KindInvalidInput},
		{"chat empty messages", func() error {
			_, err := td(t).Chat(context.Background(), provider.ChatRequest{})
			return err
		}, cascade.KindInvalidInput},
		{"count unsupported", func() error {
			_, err := td(t).Count(context.Background(), provider.CountRequest{Text: "hello"})
			return err
		}, cascade.KindUnsupported},
	} {
		if kind := kindOf(t, tc.run()); kind != tc.want {
			t.Fatalf("%s: kind = %v, want %v", tc.name, kind, tc.want)
		}
	}
}
func testErrStreamFailures(t *testing.T) {
	partial := `{"model":"llama3","message":{"role":"assistant","content":"partial"},"done":false}` + "\n"
	for _, tc := range []struct {
		name       string
		body       string
		wantKind   cascade.Kind
		wantSubstr string
	}{
		{"truncated", partial, cascade.KindIntegrity, ""},
		{"mid-stream error", partial + `{"error":"model runner crashed"}` + "\n", cascade.KindUnavailable, "crashed"},
	} {
		var terminal int
		err := streamOne(t, tc.body, func(ev provider.StreamEvent) error {
			if ev.Kind == provider.StreamEventDone || ev.Kind == provider.StreamEventError {
				terminal++
			}
			return nil
		})
		if kindOf(t, err) != tc.wantKind || terminal != 1 {
			t.Fatalf("%s: kind = %v, terminal = %d, want %v and exactly 1", tc.name, kindOf(t, err), terminal, tc.wantKind)
		}
		if tc.wantSubstr != "" && !strings.Contains(err.Error(), tc.wantSubstr) {
			t.Fatalf("%s: err = %v, want it to contain %q", tc.name, err, tc.wantSubstr)
		}
	}
	if err := streamOne(t, "not-json\n", func(provider.StreamEvent) error { return nil }); kindOf(t, err) != cascade.KindIntegrity {
		t.Fatalf("malformed chunk: kind = %v, want KindIntegrity", kindOf(t, err))
	}
	nonOK := td(t, fakeResp{status: 500, body: `{"error":"boom"}`})
	req := provider.ChatRequest{Messages: []provider.ChatMessage{{Role: "user", Content: "hi"}}}
	if err := nonOK.Stream(context.Background(), req, func(provider.StreamEvent) error { return nil }); kindOf(t, err) != cascade.KindUnavailable {
		t.Fatalf("non-ok status: kind = %v, want KindUnavailable", kindOf(t, err))
	}
	sinkErr := errors.New("sink refuses")
	if err := streamOne(t, recordedStreamNDJSON, func(provider.StreamEvent) error { return sinkErr }); !errors.Is(err, sinkErr) {
		t.Fatalf("sink abort: err = %v, want sinkErr", err)
	}
}

// FuzzOllamaWireDecode fuzzes decodeStreamChunk: it must never panic on malformed input. Go auto-loads testdata/fuzz/FuzzOllamaWireDecode/ as a seed on every run.
func FuzzOllamaWireDecode(f *testing.F) {
	for _, seed := range [][]byte{
		nil, {}, []byte("{"), []byte(`{"error":"boom"}`),
		[]byte("not-json\n{}"), []byte(`{"done":true`), []byte("\n\n\n"),
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, in []byte) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("decode panicked on %q: %v", in, r)
			}
		}()
		scanner := bufio.NewScanner(bytes.NewReader(in))
		for scanner.Scan() {
			_, _ = decodeStreamChunk(scanner.Bytes())
		}
	})
}
