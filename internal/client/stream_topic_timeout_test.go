//go:build !windows && integration

package client

// Purpose: a topic stream is long-lived, so the Client's per-call timeout
//   (New's timeout argument, http.Client.Timeout) must not bound it; only
//   ctx and the closer do, exactly as JobEvents does.

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"
)

// ctxPipeTransport ties the response body to the request context, as the
// real transport does: when the request context ends, the body read fails.
type ctxPipeTransport struct {
	pr *io.PipeReader
}

func (t *ctxPipeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	go func() {
		<-req.Context().Done()
		_ = t.pr.CloseWithError(req.Context().Err())
	}()
	return &http.Response{StatusCode: http.StatusOK, Body: t.pr}, nil
}

func TestStreamTopicOutlivesClientTimeout(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	pr, pw := io.Pipe()
	defer func() { _ = pw.Close() }()
	c := &Client{
		target:     "test",
		httpClient: &http.Client{Transport: NewPrincipalTransport(&ctxPipeTransport{pr: pr}), Timeout: 50 * time.Millisecond},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, closeFn, err := c.StreamTopic(ctx, "jobs.evidence", nil)
	if err != nil {
		t.Fatalf("StreamTopic: %v", err)
	}
	defer closeFn()

	time.Sleep(200 * time.Millisecond)
	go func() { _, _ = pw.Write([]byte("id: 1\ndata: late\n\n")) }()

	select {
	case ev, ok := <-ch:
		if !ok || ev.Err != nil || ev.Data != "late" {
			t.Fatalf("event = %+v ok=%v, want data=late with no error: the client timeout cut the stream", ev, ok)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for the late event")
	}
}
