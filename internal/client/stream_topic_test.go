//go:build !windows && integration

package client

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/pkg/cascade"
)

// sseBody is an io.ReadCloser backed by a string reader, used to feed
// scripted SSE events into streamLoop without opening a real socket.
type sseBody struct{ r io.Reader }

func (b *sseBody) Read(p []byte) (int, error) { return b.r.Read(p) }
func (b *sseBody) Close() error               { return nil }

// scriptTransport records the outgoing request and returns a scripted body.
type scriptTransport struct {
	req  *http.Request
	body string
}

func (t *scriptTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.req = req
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       &sseBody{r: strings.NewReader(t.body)},
	}, nil
}

// pipeTransport returns a pre-opened pipe body for streaming tests.
type pipeTransport struct{ body io.ReadCloser }

func (t *pipeTransport) RoundTrip(_ *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       t.body,
	}, nil
}

// statusTransport answers every request with a fixed status and empty body.
type statusTransport struct{ code int }

func (t *statusTransport) RoundTrip(_ *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: t.code,
		Body:       &sseBody{r: strings.NewReader("boom")},
	}, nil
}

// newTestClient builds a Client whose transport is replaced by rt.
func newTestClient(rt http.RoundTripper) *Client {
	return &Client{
		target:     "test",
		httpClient: &http.Client{Transport: rt},
	}
}

// unsetPrincipalToken removes the principal token from the environment for
// the rest of the test; t.Setenv first registers the restore of the old value.
func unsetPrincipalToken(t *testing.T) {
	t.Helper()
	t.Setenv(principal.EnvToken, "")
	if err := os.Unsetenv(principal.EnvToken); err != nil {
		t.Fatalf("unset %s: %v", principal.EnvToken, err)
	}
}

// TestStreamTopicQueryAndEnd verifies that StreamTopic sends the correct
// GET /events URL with principal headers, yields each event, ends with
// KindUnavailable on server close, and cancels cleanly.
func TestStreamTopicQueryAndEnd(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	t.Run("principal_token_header", testTopicPrincipalToken)
	t.Run("owner_marker_without_token", testTopicOwnerMarker)
	t.Run("yields_each_event", testTopicYieldsEvents)
	t.Run("ends_unavailable_on_server_close", testTopicServerClose)
	t.Run("non_2xx_status", testTopicNon2xx)
	t.Run("clean_cancel", testTopicCleanCancel)
}

func testTopicPrincipalToken(t *testing.T) {
	token := "cpt1_testtokenvalue"
	t.Setenv(principal.EnvToken, token)

	st := &scriptTransport{body: "id: 1\ndata: ev1\n\n"}
	c := newTestClient(NewPrincipalTransport(st))
	q := url.Values{"ticket": {"P1-X"}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, closeFn, err := c.StreamTopic(ctx, "jobs.evidence", q)
	if err != nil {
		t.Fatalf("StreamTopic: %v", err)
	}
	defer closeFn()

	got := <-ch
	if got.ID != "1" || got.Data != "ev1" {
		t.Fatalf("event = %+v, want id=1 data=ev1", got)
	}

	reqURL := st.req.URL.String()
	wantPath := unixBaseURL + eventsPath
	if !strings.HasPrefix(reqURL, wantPath+"?") {
		t.Fatalf("url = %s, want prefix %s?", reqURL, wantPath)
	}
	qv := st.req.URL.Query()
	if qv.Get("topic") != "jobs.evidence" {
		t.Fatalf("topic = %q, want jobs.evidence", qv.Get("topic"))
	}
	if qv.Get("ticket") != "P1-X" {
		t.Fatalf("ticket = %q, want P1-X", qv.Get("ticket"))
	}
	if h := st.req.Header.Get(principal.HeaderToken); h != token {
		t.Fatalf("header %s = %q, want %q", principal.HeaderToken, h, token)
	}
}

func testTopicOwnerMarker(t *testing.T) {
	unsetPrincipalToken(t)

	st := &scriptTransport{body: "id: 2\ndata: ev2\n\n"}
	c := newTestClient(NewPrincipalTransport(st))
	q := url.Values{"ticket": {"P1-X"}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, closeFn, err := c.StreamTopic(ctx, "jobs.evidence", q)
	if err != nil {
		t.Fatalf("StreamTopic: %v", err)
	}
	defer closeFn()

	<-ch

	if h := st.req.Header.Get(principal.HeaderOwnerCLI); h != "owner-cli" {
		t.Fatalf("header %s = %q, want owner-cli", principal.HeaderOwnerCLI, h)
	}
	if h := st.req.Header.Get(principal.HeaderToken); h != "" {
		t.Fatalf("header %s = %q, want empty", principal.HeaderToken, h)
	}
}

func testTopicYieldsEvents(t *testing.T) {
	body := "id: 1\ndata: a\n\nid: 2\ndata: b\n\n"
	st := &scriptTransport{body: body}
	c := newTestClient(NewPrincipalTransport(st))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, closeFn, err := c.StreamTopic(ctx, "t", nil)
	if err != nil {
		t.Fatalf("StreamTopic: %v", err)
	}
	defer closeFn()

	var evs []Event
	var last Event
	for ev := range ch {
		last = ev
		if ev.Err == nil {
			evs = append(evs, ev)
		}
	}
	if len(evs) != 2 {
		t.Fatalf("got %d data events, want 2", len(evs))
	}
	if evs[0].Data != "a" || evs[1].Data != "b" {
		t.Fatalf("events = %+v, want a then b", evs)
	}
	if !cascade.HasKind(last.Err, cascade.KindUnavailable) {
		t.Fatalf("last event err = %v, want KindUnavailable end", last.Err)
	}
}

func testTopicServerClose(t *testing.T) {
	pr, pw := io.Pipe()
	defer func() { _ = pw.Close() }()
	st := &pipeTransport{body: pr}
	c := newTestClient(NewPrincipalTransport(st))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, closeFn, err := c.StreamTopic(ctx, "t", nil)
	if err != nil {
		t.Fatalf("StreamTopic: %v", err)
	}
	defer closeFn()

	_, _ = pw.Write([]byte("id: 1\ndata: x\n\n"))
	if ev := <-ch; ev.Data != "x" {
		t.Fatalf("first event = %+v, want data=x", ev)
	}

	if err := pw.Close(); err != nil {
		t.Fatalf("close pipe: %v", err)
	}

	select {
	case ev := <-ch:
		if ev.Err == nil {
			t.Fatalf("final event = %+v, want Err", ev)
		}
		if k, ok := cascade.KindOf(ev.Err); !ok || k != cascade.KindUnavailable {
			t.Fatalf("kind = %v ok=%v, want KindUnavailable", k, ok)
		}
		if !strings.Contains(ev.Err.Error(), "closed by daemon") {
			t.Fatalf("err = %q, want 'closed by daemon'", ev.Err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for final event")
	}
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("channel not closed after final event")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for channel close")
	}
}

func testTopicNon2xx(t *testing.T) {
	c := newTestClient(NewPrincipalTransport(&statusTransport{code: http.StatusServiceUnavailable}))
	ch, closeFn, err := c.StreamTopic(context.Background(), "t", nil)
	defer closeFn()
	if ch != nil {
		t.Fatal("channel not nil on non-2xx")
	}
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("err = %v, want KindUnavailable", err)
	}
}

func testTopicCleanCancel(t *testing.T) {
	pr, pw := io.Pipe()
	defer func() { _ = pw.Close() }()
	st := &pipeTransport{body: pr}
	c := newTestClient(NewPrincipalTransport(st))

	ctx, cancel := context.WithCancel(context.Background())

	ch, closeFn, err := c.StreamTopic(ctx, "t", nil)
	if err != nil {
		t.Fatalf("StreamTopic: %v", err)
	}
	defer closeFn()

	cancel()
	if err := pw.Close(); err != nil {
		t.Fatalf("close pipe: %v", err)
	}

	deadline := time.After(2 * time.Second)
	for done := false; !done; {
		select {
		case ev, ok := <-ch:
			if !ok {
				done = true
			} else if ev.Err != nil {
				t.Fatalf("cancel produced error event: %v", ev.Err)
			}
		case <-deadline:
			t.Fatal("timeout waiting for cancel close")
		}
	}
}
