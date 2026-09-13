package transport

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/acamarata/cascade/providers/anthropic"
	"github.com/acamarata/cascade/providers/gemini"
	"github.com/acamarata/cascade/providers/ollama"
)

// fakeTransport is a Transport double: no socket, no net/http import,
// records the call it received and returns a canned result. Every test
// in this package that needs a Transport - including the ones that build
// all four providers/* drivers - uses this instead of a real *http.Client,
// which is exactly what keeps this package's own tests off the "net"/
// "net/http" import ban (internal/build/hygiene.go).
type fakeTransport struct {
	gotMethod  string
	gotURL     string
	gotHeaders map[string]string
	gotBody    []byte
	status     int
	respHdrs   map[string][]string
	body       string
	err        error
}

func (f *fakeTransport) Send(_ context.Context, method, url string, headers map[string]string, body []byte) (int, map[string][]string, io.ReadCloser, error) {
	f.gotMethod, f.gotURL, f.gotHeaders, f.gotBody = method, url, headers, body
	if f.err != nil {
		return 0, nil, nil, f.err
	}
	return f.status, f.respHdrs, io.NopCloser(strings.NewReader(f.body)), nil
}

func TestAnthropicDoer_TranslatesRequestAndResponse(t *testing.T) {
	ft := &fakeTransport{status: 200, body: "ok"}
	d := AnthropicDoer{Transport: ft}
	resp, err := d.Do(context.Background(), anthropic.HTTPRequest{
		Method: "POST", URL: "https://example.invalid/v1/messages",
		Headers: map[string]string{"x-api-key": "k"}, Body: []byte(`{"a":1}`),
	})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if ft.gotMethod != "POST" || ft.gotURL != "https://example.invalid/v1/messages" {
		t.Fatalf("transport got method=%q url=%q, want POST/the request URL", ft.gotMethod, ft.gotURL)
	}
	if ft.gotHeaders["x-api-key"] != "k" || string(ft.gotBody) != `{"a":1}` {
		t.Fatalf("transport did not receive the request's headers/body: %+v %q", ft.gotHeaders, ft.gotBody)
	}
	if resp.Status != 200 {
		t.Fatalf("resp.Status = %d, want 200", resp.Status)
	}
	got, _ := io.ReadAll(resp.Body)
	if string(got) != "ok" {
		t.Fatalf("resp.Body = %q, want ok", got)
	}
}

func TestAnthropicDoer_PropagatesTransportError(t *testing.T) {
	ft := &fakeTransport{err: errors.New("boom")}
	d := AnthropicDoer{Transport: ft}
	if _, err := d.Do(context.Background(), anthropic.HTTPRequest{Method: "GET", URL: "x"}); err == nil {
		t.Fatal("Do: want error, got nil")
	}
}

func TestGeminiDoer_TranslatesRequestAndResponse(t *testing.T) {
	ft := &fakeTransport{status: 201, body: "g"}
	d := GeminiDoer{Transport: ft}
	resp, err := d.Do(context.Background(), gemini.HTTPRequest{Method: "POST", URL: "u", Body: []byte("b")})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if resp.Status != 201 || ft.gotMethod != "POST" || string(ft.gotBody) != "b" {
		t.Fatalf("resp=%+v transport method=%q body=%q", resp, ft.gotMethod, ft.gotBody)
	}
}

func TestOllamaDoer_TranslatesRequestAndResponse(t *testing.T) {
	ft := &fakeTransport{status: 200, body: "o"}
	d := OllamaDoer{Transport: ft}
	resp, err := d.Do(context.Background(), ollama.HTTPRequest{Method: "GET", URL: "u"})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if resp.Status != 200 || ft.gotMethod != "GET" {
		t.Fatalf("resp=%+v transport method=%q", resp, ft.gotMethod)
	}
}

func TestGeminiDoer_PropagatesTransportError(t *testing.T) {
	ft := &fakeTransport{err: errors.New("boom")}
	d := GeminiDoer{Transport: ft}
	if _, err := d.Do(context.Background(), gemini.HTTPRequest{Method: "GET", URL: "x"}); err == nil {
		t.Fatal("Do: want error, got nil")
	}
}

func TestOllamaDoer_PropagatesTransportError(t *testing.T) {
	ft := &fakeTransport{err: errors.New("boom")}
	d := OllamaDoer{Transport: ft}
	if _, err := d.Do(context.Background(), ollama.HTTPRequest{Method: "GET", URL: "x"}); err == nil {
		t.Fatal("Do: want error, got nil")
	}
}

func TestRequestBody_EmptyIsNil(t *testing.T) {
	if got := requestBody(nil); got != nil {
		t.Fatalf("requestBody(nil) = %v, want nil", got)
	}
	if got := requestBody([]byte{}); got != nil {
		t.Fatalf("requestBody(empty) = %v, want nil", got)
	}
}

func TestRequestBody_NonEmptyReadsBack(t *testing.T) {
	r := requestBody([]byte("payload"))
	if r == nil {
		t.Fatal("requestBody(non-empty) = nil, want a reader")
	}
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != "payload" {
		t.Fatalf("got %q, want payload", got)
	}
}

func TestFlattenHeader_TakesFirstValue(t *testing.T) {
	got := flattenHeader(map[string][]string{"Retry-After": {"5", "10"}, "Empty": {}})
	if got["Retry-After"] != "5" {
		t.Fatalf("Retry-After = %q, want 5", got["Retry-After"])
	}
	if _, ok := got["Empty"]; ok {
		t.Fatalf("Empty should be dropped for a zero-length value slice, got %v", got)
	}
}

func TestHeaderSlice_WrapsEachValue(t *testing.T) {
	got := headerSlice(map[string]string{"x-api-key": "k"})
	if len(got["x-api-key"]) != 1 || got["x-api-key"][0] != "k" {
		t.Fatalf("headerSlice = %v, want a single-element slice [k]", got["x-api-key"])
	}
}

func TestHeaderSlice_EmptyInputEmptyOutput(t *testing.T) {
	if got := headerSlice(nil); len(got) != 0 {
		t.Fatalf("headerSlice(nil) = %v, want empty", got)
	}
}

func TestReadBody_NilReturnsNilNil(t *testing.T) {
	got, err := readBody(nil)
	if err != nil || got != nil {
		t.Fatalf("readBody(nil) = (%v, %v), want (nil, nil)", got, err)
	}
}

func TestReadBody_DrainsReadCloser(t *testing.T) {
	got, err := readBody(io.NopCloser(strings.NewReader("payload")))
	if err != nil {
		t.Fatalf("readBody: %v", err)
	}
	if string(got) != "payload" {
		t.Fatalf("readBody = %q, want payload", got)
	}
}

func TestReadBody_PropagatesReadError(t *testing.T) {
	if _, err := readBody(io.NopCloser(&errReader{})); err == nil {
		t.Fatal("readBody: want error from a failing reader, got nil")
	}
}

// errReader is an io.Reader that always fails, so readBody's error path is
// exercised without opening a socket.
type errReader struct{}

func (*errReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }

func TestNewHTTPTransport_WrapsTheGivenClient(t *testing.T) {
	// Passing nil needs no net/http import: the parameter type is inferred
	// from NewHTTPTransport's own signature, not named here. This still
	// exercises the real constructor in the default unit lane.
	got := NewHTTPTransport(nil)
	nt, ok := got.(netTransport)
	if !ok {
		t.Fatalf("NewHTTPTransport returned %T, want netTransport", got)
	}
	if nt.client != nil {
		t.Fatalf("nt.client = %v, want nil (the client passed in)", nt.client)
	}
}
