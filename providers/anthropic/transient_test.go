package anthropic

// Purpose (this file): the driver marks exactly the transient shapes - the
//   constructed fixtures, the retryable 5xx set, the SSE error event and a
//   transport reset with a live context - and nothing else.
// Inputs: internal/conductor/testdata/transient (constructed; see README).
// SPORT: placeholder: providers/anthropic driver (TEST, P1-GWY-03).

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// transientFixtures is the shared constructed fixture set.
var transientFixtures = filepath.Join("..", "..", "internal", "conductor", "testdata", "transient")

func readTransientFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(transientFixtures, name))
	if err != nil {
		t.Fatalf("reading fixture %s: %v", name, err)
	}
	return string(b)
}

const constructed5xxBody = `{"type":"error","error":{"type":"api_error","message":"upstream failure"}}`

var chatReq = provider.ChatRequest{Messages: []provider.ChatMessage{{Role: "user", Content: "x"}}}

// chatStatus serves one status and body to Chat and returns its error.
func chatStatus(t *testing.T, status int, body string) error {
	t.Helper()
	_, err := keyAuthDriver(t, &fakeDoer{queue: []fakeResp{{status: status, body: body}}}).Chat(context.Background(), chatReq)
	if err == nil {
		t.Fatalf("status %d: Chat succeeded, want an error", status)
	}
	return err
}

// wantTransient asserts err carries the mark with cause and status.
func wantTransient(t *testing.T, label string, err error, cause provider.TransientCause, status int) {
	t.Helper()
	info, ok := provider.TransientOf(err)
	if !ok || info.Cause != cause || info.Status != status {
		t.Fatalf("%s: TransientOf = %+v, %v; want cause %q status %d", label, info, ok, cause, status)
	}
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("%s: kind of %v changed under the mark", label, err)
	}
}

// resetDoer fails every request with err, whatever the context state.
type resetDoer struct{ err error }

func (r resetDoer) Do(context.Context, HTTPRequest) (HTTPResponse, error) {
	return HTTPResponse{}, r.err
}

func TestAnthropicMarksTransient(t *testing.T) {
	t.Run("fixtures", func(t *testing.T) {
		wantTransient(t, "500", chatStatus(t, 500, readTransientFixture(t, "transient_500.json")), provider.TransientHTTP5xx, 500)
		wantTransient(t, "503", chatStatus(t, 503, readTransientFixture(t, "transient_503.json")), provider.TransientHTTP5xx, 503)
		wantTransient(t, "529", chatStatus(t, 529, readTransientFixture(t, "transient_529.json")), provider.TransientOverloaded, 529)
		err := chatStatus(t, 401, readTransientFixture(t, "unauthorized_401.json"))
		if _, ok := provider.TransientOf(err); ok || !cascade.HasKind(err, cascade.KindPermissionDenied) {
			t.Fatalf("401 fixture: %v marked transient or not permission-denied", err)
		}
	})
	t.Run("constructed_statuses", func(t *testing.T) {
		for _, s := range []int{500, 502, 503, 504, 520, 521, 522, 523, 524} {
			wantTransient(t, fmt.Sprint(s), chatStatus(t, s, constructed5xxBody), provider.TransientHTTP5xx, s)
		}
		wantTransient(t, "529", chatStatus(t, 529, constructed5xxBody), provider.TransientOverloaded, 529)
		wantTransient(t, "500 overloaded body", chatStatus(t, 500, readTransientFixture(t, "transient_529.json")), provider.TransientOverloaded, 500)
		wantTransient(t, "503 no body", chatStatus(t, 503, ""), provider.TransientHTTP5xx, 503)
	})
	t.Run("not_transient_statuses", func(t *testing.T) {
		for _, s := range []int{501, 505, 400, 401, 403, 404, 413, 429} {
			err := chatStatus(t, s, constructed5xxBody)
			if _, ok := provider.TransientOf(err); ok {
				t.Fatalf("status %d marked transient: %v", s, err)
			}
		}
		overloaded4xx := chatStatus(t, 429, readTransientFixture(t, "transient_529.json"))
		if _, ok := provider.TransientOf(overloaded4xx); ok || !cascade.HasKind(overloaded4xx, cascade.KindQuotaExhausted) {
			t.Fatalf("429 with an overloaded body: %v, want quota-exhausted and unmarked", overloaded4xx)
		}
	})
	t.Run("stream_error_event", testStreamErrorEventMarks)
	t.Run("transport", testTransportMarks)
}

func testStreamErrorEventMarks(t *testing.T) {
	for _, name := range []string{"transient_stream_api_error.sse", "transient_stream_overloaded.sse"} {
		doer := &fakeDoer{queue: []fakeResp{{status: 200, body: readTransientFixture(t, name)}}}
		var evs []provider.StreamEvent
		err := keyAuthDriver(t, doer).Stream(context.Background(), chatReq, func(ev provider.StreamEvent) error {
			evs = append(evs, ev)
			return nil
		})
		info, ok := provider.TransientOf(err)
		if !ok || info.Cause != provider.TransientStreamAPIError || info.Status != 0 {
			t.Fatalf("%s: TransientOf = %+v, %v; want stream_api_error", name, info, ok)
		}
		if len(evs) != 1 || evs[0].Kind != provider.StreamEventError || evs[0].Err != err {
			t.Fatalf("%s: events = %+v, want the one error event carrying the returned error", name, evs)
		}
	}
	other := "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"invalid_request_error\",\"message\":\"bad\"}}\n\n"
	err := keyAuthDriver(t, &fakeDoer{queue: []fakeResp{{status: 200, body: other}}}).Stream(context.Background(), chatReq,
		func(provider.StreamEvent) error { return nil })
	if _, ok := provider.TransientOf(err); ok || err == nil {
		t.Fatalf("invalid_request_error event: %v, want an unmarked error", err)
	}
}

func testTransportMarks(t *testing.T) {
	resets := []error{
		fmt.Errorf("read tcp: %w", syscall.ECONNRESET),
		fmt.Errorf("post: %w", io.EOF),
		fmt.Errorf("post: %w", io.ErrUnexpectedEOF),
	}
	for _, raw := range resets {
		d := keyAuthDriver(t, &fakeDoer{})
		d.cfg.Doer = resetDoer{err: raw}
		_, err := d.Chat(context.Background(), chatReq)
		info, ok := provider.TransientOf(err)
		if !ok || info.Cause != provider.TransientTransport || info.Status != 0 || !cascade.HasKind(err, cascade.KindUnavailable) {
			t.Fatalf("%v: TransientOf = %+v, %v (err %v); want transport", raw, info, ok, err)
		}
	}
	d := keyAuthDriver(t, &fakeDoer{})
	d.cfg.Doer = resetDoer{err: errors.New("dial: no route")}
	if _, err := d.Chat(context.Background(), chatReq); err == nil {
		t.Fatal("dial failure succeeded")
	} else if _, ok := provider.TransientOf(err); ok {
		t.Fatalf("a non-reset transport failure is marked: %v", err)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, cancel2 := context.WithDeadline(context.Background(), time.Unix(1, 0))
	defer cancel2()
	for _, ctx := range []context.Context{cancelled, expired} {
		d := keyAuthDriver(t, &fakeDoer{})
		d.cfg.Doer = resetDoer{err: fmt.Errorf("read tcp: %w", syscall.ECONNRESET)}
		_, err := d.Chat(ctx, chatReq)
		if _, ok := provider.TransientOf(err); ok || err == nil {
			t.Fatalf("ctx %v: reset marked transient: %v", ctx.Err(), err)
		}
	}
}
