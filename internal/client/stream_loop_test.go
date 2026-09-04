package client

// Purpose: the SSE read loop's own unit tests — startStream/streamLoop
//   driven over a scripted event-stream body, so the framing contract
//   (multi-event blocks, resume-token ids, heartbeat comment lines, a
//   mid-stream disconnect, context cancellation, channel close) is
//   asserted without a socket. The same paths over a REAL
//   internal/rpc.SSEHandler and a REAL events.Bus are
//   stream_integration_test.go's; this file imports neither "net" nor
//   "net/http" so it runs in the default no-network unit lane.
// SPORT: internal/client (ADD).

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// scriptedBody is an io.ReadCloser serving a fixed event-stream
// transcript, optionally failing partway (failAfter bytes) to model a
// daemon that dies mid-stream. It records whether Close was called, so
// the closer startStream hands back is proven to reach the connection.
type scriptedBody struct {
	data      string
	offset    int
	failAfter int
	closed    bool
}

var errStreamDropped = errors.New("connection dropped mid-stream")

func (b *scriptedBody) Read(p []byte) (int, error) {
	if b.failAfter > 0 && b.offset >= b.failAfter {
		return 0, errStreamDropped
	}
	if b.offset >= len(b.data) {
		return 0, io.EOF
	}
	end := len(b.data)
	if b.failAfter > 0 && b.failAfter < end {
		end = b.failAfter
	}
	n := copy(p, b.data[b.offset:end])
	b.offset += n
	return n, nil
}

func (b *scriptedBody) Close() error {
	b.closed = true
	return nil
}

// collect drains ch until it closes or the test times out, returning every
// event delivered. A stream that never closes fails the test rather than
// hanging the package.
func collect(t *testing.T, ch <-chan sseEvent) []sseEvent {
	t.Helper()
	var got []sseEvent
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return got
			}
			got = append(got, ev)
		case <-deadline:
			t.Fatalf("event channel never closed; got %d event(s) so far", len(got))
			return got
		}
	}
}

// TestStartStream_DeliversEventsWithResumeTokens proves each block's id
// (the daemon's opaque resume token) travels with its data, and that two
// blocks arrive as two distinct events in order.
func TestStartStream_DeliversEventsWithResumeTokens(t *testing.T) {
	body := &scriptedBody{data: "id: tok-1\ndata: {\"n\":1}\n\nid: tok-2\ndata: {\"n\":2}\n\n"}
	ch, closeFn := startStream(context.Background(), body)
	defer closeFn()

	got := collect(t, ch)
	if len(got) != 2 {
		t.Fatalf("got %d event(s), want 2: %+v", len(got), got)
	}
	if got[0].ID != "tok-1" || got[0].Data != `{"n":1}` {
		t.Errorf("event 0 = %+v, want {ID:tok-1 Data:{\"n\":1}}", got[0])
	}
	if got[1].ID != "tok-2" || got[1].Data != `{"n":2}` {
		t.Errorf("event 1 = %+v, want {ID:tok-2 Data:{\"n\":2}}", got[1])
	}
}

// TestStartStream_HeartbeatsAreNotEvents proves the keep-alive comment
// lines the daemon writes between events are swallowed: a subscriber must
// never see a heartbeat as an event, and the events either side of it must
// still arrive intact.
func TestStartStream_HeartbeatsAreNotEvents(t *testing.T) {
	body := &scriptedBody{data: ": keep-alive\n\nid: tok-1\ndata: one\n\n: keep-alive\n\n"}
	ch, closeFn := startStream(context.Background(), body)
	defer closeFn()

	got := collect(t, ch)
	if len(got) != 1 {
		t.Fatalf("got %d event(s), want exactly 1 (heartbeats are not events): %+v", len(got), got)
	}
	if got[0].ID != "tok-1" || got[0].Data != "one" {
		t.Errorf("event = %+v, want {ID:tok-1 Data:one}", got[0])
	}
}

// TestStartStream_MultilineDataJoined proves a block carrying several data
// lines is delivered as ONE event whose payload joins them with newlines,
// per the SSE spec, rather than as several truncated events.
func TestStartStream_MultilineDataJoined(t *testing.T) {
	body := &scriptedBody{data: "id: tok-1\ndata: first\ndata: second\n\n"}
	ch, closeFn := startStream(context.Background(), body)
	defer closeFn()

	got := collect(t, ch)
	if len(got) != 1 {
		t.Fatalf("got %d event(s), want 1: %+v", len(got), got)
	}
	if got[0].Data != "first\nsecond" {
		t.Errorf("Data = %q, want %q", got[0].Data, "first\nsecond")
	}
}

// TestStartStream_MidStreamDisconnect proves a connection that dies partway
// through a block delivers every COMPLETE event before the break and never
// the half-written one, then closes the channel so the caller's range
// terminates instead of hanging.
func TestStartStream_MidStreamDisconnect(t *testing.T) {
	transcript := "id: tok-1\ndata: complete\n\nid: tok-2\ndata: half-writ"
	body := &scriptedBody{data: transcript, failAfter: len(transcript)}
	ch, closeFn := startStream(context.Background(), body)
	defer closeFn()

	got := collect(t, ch)
	if len(got) != 1 {
		t.Fatalf("got %d event(s), want only the one complete block: %+v", len(got), got)
	}
	if got[0].ID != "tok-1" || got[0].Data != "complete" {
		t.Errorf("event = %+v, want the completed block {ID:tok-1 Data:complete}", got[0])
	}
}

// TestStartStream_EOFWithUnterminatedBlock proves a clean EOF partway
// through a block also drops the unterminated event rather than emitting a
// partial one.
func TestStartStream_EOFWithUnterminatedBlock(t *testing.T) {
	body := &scriptedBody{data: "id: tok-1\ndata: never-terminated\n"}
	ch, closeFn := startStream(context.Background(), body)
	defer closeFn()

	if got := collect(t, ch); len(got) != 0 {
		t.Fatalf("got %+v, want no event for an unterminated block", got)
	}
}

// TestStartStream_BlankBlocksSkipped proves consecutive blank lines do not
// produce empty events.
func TestStartStream_BlankBlocksSkipped(t *testing.T) {
	body := &scriptedBody{data: "\n\n\nid: tok-1\ndata: real\n\n\n"}
	ch, closeFn := startStream(context.Background(), body)
	defer closeFn()

	got := collect(t, ch)
	if len(got) != 1 || got[0].ID != "tok-1" {
		t.Fatalf("got %+v, want exactly the one real event", got)
	}
}

// TestStartStream_CloseFnClosesTheBody proves the returned closer reaches
// the underlying connection, so a caller that defers it does not leak the
// socket.
func TestStartStream_CloseFnClosesTheBody(t *testing.T) {
	body := &scriptedBody{data: "id: tok-1\ndata: one\n\n"}
	ch, closeFn := startStream(context.Background(), body)
	collect(t, ch)
	closeFn()
	if !body.closed {
		t.Error("closeFn did not close the response body")
	}
}

// TestStartStream_CanceledContextStopsBeforeDelivery proves a context
// canceled before the loop runs delivers nothing and still closes the
// channel: cancellation must not strand a subscriber.
func TestStartStream_CanceledContextStopsBeforeDelivery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	body := &scriptedBody{data: strings.Repeat("id: t\ndata: d\n\n", 50)}

	ch, closeFn := startStream(ctx, body)
	defer closeFn()

	if got := collect(t, ch); len(got) != 0 {
		t.Fatalf("got %d event(s) on a canceled context, want none", len(got))
	}
}
