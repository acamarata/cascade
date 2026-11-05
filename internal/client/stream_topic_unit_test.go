package client

// Purpose: the no-network unit lane for stream_topic.go. Every helper the
//   topic stream is built from (URL building, status check, event
//   forwarding) is pure, so it is asserted here without "net" or
//   "net/http"; StreamTopic itself is driven at a unix socket path nothing
//   serves, a connect(2) failure before any byte moves. The scripted
//   transport path is stream_topic_test.go's (integration tag).
// SPORT: internal/client (ADD).

import (
	"context"
	"io"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

type recordCloser struct{ closed bool }

func (r *recordCloser) Close() error { r.closed = true; return nil }

func TestTopicURL_TopicFirstClassAndQueryMerged(t *testing.T) {
	got := topicURL("jobs.evidence", url.Values{"ticket": {"P1-X"}})
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse %q: %v", got, err)
	}
	if !strings.HasPrefix(got, unixBaseURL+eventsPath+"?") {
		t.Fatalf("url = %s, want prefix %s?", got, unixBaseURL+eventsPath)
	}
	if want := "ticket=P1-X&topic=jobs.evidence"; u.RawQuery != want {
		t.Fatalf("query = %q, want %q", u.RawQuery, want)
	}
	if bare := topicURL("t", nil); !strings.HasSuffix(bare, "?topic=t") {
		t.Fatalf("nil query url = %s", bare)
	}
}

func TestTopicURL_QueryTopicPrecedence(t *testing.T) {
	got := topicURL("a", url.Values{"topic": {"b"}})
	if !strings.HasSuffix(got, "?topic=a") {
		t.Fatalf("url = %s, want the topic argument to win over a query topic key", got)
	}
}

func TestCheckTopicResponse(t *testing.T) {
	for _, code := range []int{200, 204, 299} {
		rc := &recordCloser{}
		if err := checkTopicResponse(code, rc, "tgt"); err != nil {
			t.Fatalf("status %d: err = %v, want nil", code, err)
		}
		if rc.closed {
			t.Fatalf("status %d: body closed on success", code)
		}
	}
	for _, code := range []int{199, 300, 404, 503} {
		rc := &recordCloser{}
		err := checkTopicResponse(code, rc, "tgt")
		if !cascade.HasKind(err, cascade.KindUnavailable) {
			t.Fatalf("status %d: err = %v, want KindUnavailable", code, err)
		}
		if !strings.Contains(err.Error(), "HTTP "+itoa(code)) || !strings.Contains(err.Error(), "tgt") {
			t.Fatalf("status %d: err = %q, want status and target", code, err)
		}
		if !rc.closed {
			t.Fatalf("status %d: body not closed", code)
		}
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}

func TestForwardTopic_CopiesThenReportsDaemonClose(t *testing.T) {
	inner := make(chan sseEvent, 2)
	inner <- sseEvent{ID: "1", Data: "a"}
	inner <- sseEvent{ID: "2", Data: "b"}
	close(inner)
	out := make(chan Event)
	go forwardTopic(context.Background(), inner, out)

	var got []Event
	timeout := time.After(2 * time.Second)
	for done := false; !done; {
		select {
		case ev, ok := <-out:
			if !ok {
				done = true
				break
			}
			got = append(got, ev)
		case <-timeout:
			t.Fatal("timeout waiting for forwardTopic")
		}
	}
	if len(got) != 3 {
		t.Fatalf("got %d events, want 3: %+v", len(got), got)
	}
	if got[0].ID != "1" || got[0].Data != "a" || got[1].ID != "2" || got[1].Data != "b" {
		t.Fatalf("events out of order: %+v", got)
	}
	last := got[2]
	if k, ok := cascade.KindOf(last.Err); !ok || k != cascade.KindUnavailable {
		t.Fatalf("final err kind = %v ok=%v, want KindUnavailable", k, ok)
	}
	if !strings.Contains(last.Err.Error(), "closed by daemon") {
		t.Fatalf("final err = %q, want 'closed by daemon'", last.Err)
	}
}

func TestForwardTopic_CanceledContextEndsCleanly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	inner := make(chan sseEvent)
	close(inner)
	out := make(chan Event)
	go forwardTopic(ctx, inner, out)
	select {
	case ev, ok := <-out:
		if ok {
			t.Fatalf("got event %+v, want a clean close", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for close")
	}
}

func TestForwardTopic_CancelWhileBlockedOnSend(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	inner := make(chan sseEvent, 1)
	inner <- sseEvent{ID: "1", Data: "a"}
	out := make(chan Event) // never read: the send blocks
	done := make(chan struct{})
	go func() { forwardTopic(ctx, inner, out); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("forwardTopic did not return after cancel")
	}
}

func TestForwardTopic_CancelWhileBlockedOnFinalEvent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	inner := make(chan sseEvent)
	close(inner)
	out := make(chan Event) // never read: the final send blocks
	done := make(chan struct{})
	go func() { forwardTopic(ctx, inner, out); close(done) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("forwardTopic did not return after cancel")
	}
}

func TestStreamTopic_MissingSocketIsUnavailable(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	c := New(filepath.Join(t.TempDir(), "nothing-here.sock"), UnixDialer, 2*time.Second)
	ch, closeFn, err := c.StreamTopic(context.Background(), "t", nil)
	defer closeFn()
	if ch != nil {
		t.Fatal("channel not nil on dial failure")
	}
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("err = %v, want KindUnavailable", err)
	}
}

var _ io.Closer = (*recordCloser)(nil)

type readCloser struct {
	io.Reader
	closed bool
}

func (r *readCloser) Close() error { r.closed = true; return nil }

func TestOpenTopicStream_YieldsThenReportsClose(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, closeFn := openTopicStream(ctx, &readCloser{Reader: strings.NewReader("id: 1\ndata: a\n\n")})
	defer closeFn()
	var evs []Event
	timeout := time.After(2 * time.Second)
	for done := false; !done; {
		select {
		case ev, ok := <-ch:
			if !ok {
				done = true
				break
			}
			evs = append(evs, ev)
		case <-timeout:
			t.Fatal("timeout")
		}
	}
	if len(evs) != 2 || evs[0].Data != "a" || !cascade.HasKind(evs[1].Err, cascade.KindUnavailable) {
		t.Fatalf("events = %+v, want data a then KindUnavailable end", evs)
	}
}

func TestAcceptTopicBody(t *testing.T) {
	ok := &readCloser{Reader: strings.NewReader("")}
	got, err := acceptTopicBody(200, ok, "tgt")
	if err != nil || got != io.ReadCloser(ok) || ok.closed {
		t.Fatalf("2xx: got=%v err=%v closed=%v", got, err, ok.closed)
	}
	bad := &readCloser{Reader: strings.NewReader("")}
	got, err = acceptTopicBody(503, bad, "tgt")
	if got != nil || !cascade.HasKind(err, cascade.KindUnavailable) || !bad.closed {
		t.Fatalf("503: got=%v err=%v closed=%v", got, err, bad.closed)
	}
}

func TestNewTopicRequest(t *testing.T) {
	req, err := newTopicRequest(context.Background(), "jobs.evidence", url.Values{"ticket": {"P1-X"}})
	if err != nil {
		t.Fatalf("newTopicRequest: %v", err)
	}
	if req.Method != "GET" || req.URL.Query().Get("topic") != "jobs.evidence" || req.URL.Query().Get("ticket") != "P1-X" {
		t.Fatalf("request = %s %s", req.Method, req.URL)
	}
	var nilCtx context.Context
	if _, err := newTopicRequest(nilCtx, "t", nil); !cascade.HasKind(err, cascade.KindInternal) {
		t.Fatalf("nil ctx err = %v, want KindInternal", err)
	}
}
