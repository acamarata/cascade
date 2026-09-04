// Purpose: unit tests for stream.go's pure SSE line accumulator
//
//	(sseAccumulator.feed, splitSSEField) — split out of client_test.go to
//	stay under the 300-line file cap. No real socket or HTTP client;
//	stream_integration_test.go (build-tagged "integration") covers the
//	real-dial cases.
//
// SPORT: internal/client (ADD, per T-3 sport_updates).
package client

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestSSEAccumulator_Feed_SingleEvent(t *testing.T) {
	var acc sseAccumulator
	if _, complete := acc.feed("id: A1"); complete {
		t.Fatal("feed(id) reported complete, want false")
	}
	if _, complete := acc.feed("data: hello"); complete {
		t.Fatal("feed(data) reported complete, want false")
	}
	ev, complete := acc.feed("")
	if !complete {
		t.Fatal("feed(\"\") reported complete=false, want true")
	}
	if ev.ID != "A1" || ev.Data != "hello" {
		t.Errorf("event = %+v, want {ID:A1 Data:hello}", ev)
	}
}

func TestSSEAccumulator_Feed_MultilineData(t *testing.T) {
	var acc sseAccumulator
	_, _ = acc.feed("data: line1")
	_, _ = acc.feed("data: line2")
	ev, complete := acc.feed("")
	if !complete {
		t.Fatal("expected complete=true")
	}
	if ev.Data != "line1\nline2" {
		t.Errorf("Data = %q, want %q", ev.Data, "line1\nline2")
	}
}

func TestSSEAccumulator_Feed_CommentIgnored(t *testing.T) {
	var acc sseAccumulator
	if ev, complete := acc.feed(": keep-alive"); complete || ev != (sseEvent{}) {
		t.Errorf("feed(comment) = (%+v, %v), want zero value, false", ev, complete)
	}
	// A comment must not itself accumulate into the next real block.
	_, _ = acc.feed("id: x")
	ev, complete := acc.feed("")
	if !complete || ev.ID != "x" {
		t.Errorf("event after comment = %+v, want ID=x", ev)
	}
}

func TestSSEAccumulator_Feed_UnknownFieldIgnored(t *testing.T) {
	var acc sseAccumulator
	_, _ = acc.feed("event: something")
	ev, complete := acc.feed("")
	if !complete {
		t.Fatal("expected complete=true")
	}
	if ev.ID != "" || ev.Data != "" {
		t.Errorf("event = %+v, want zero value (unknown field ignored)", ev)
	}
}

func TestSplitSSEField(t *testing.T) {
	cases := []struct {
		line, field, value string
	}{
		{"id: 1", "id", "1"},
		{"id:1", "id", "1"},
		{"id", "id", ""},
		{"data:  two spaces", "data", " two spaces"},
	}
	for _, tc := range cases {
		field, value := splitSSEField(tc.line)
		if field != tc.field || value != tc.value {
			t.Errorf("splitSSEField(%q) = (%q, %q), want (%q, %q)", tc.line, field, value, tc.field, tc.value)
		}
	}
}

// TestEventsURL proves the job filter's wire shape: an empty jobID
// subscribes to everything (no query at all), a set one filters by the
// job_id parameter the daemon's SSE handler reads.
func TestEventsURL(t *testing.T) {
	if got := eventsURL(""); got != unixBaseURL+eventsPath {
		t.Errorf("eventsURL(\"\") = %q, want %q", got, unixBaseURL+eventsPath)
	}
	if got := eventsURL("job-7"); got != unixBaseURL+eventsPath+"?job_id=job-7" {
		t.Errorf("eventsURL(job-7) = %q, want the job_id filter", got)
	}
}

// TestStreamClient_Open_UnreachableSocket proves open reports the same
// taxonomy classification the request/response half does when the daemon
// is not there, hands back a non-nil no-op closer (so a caller's defer is
// safe), and never returns a channel a caller could block on forever.
func TestStreamClient_Open_UnreachableSocket(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "no-daemon.sock")
	sc := newStreamClient(sock, UnixDialer)

	ch, closeFn, err := sc.open(context.Background(), "", "")
	if err == nil {
		t.Fatal("open: expected an error dialing a socket nothing serves")
	}
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Errorf("err = %v, want KindUnavailable", err)
	}
	if ch != nil {
		t.Error("open returned a non-nil event channel alongside an error")
	}
	if closeFn == nil {
		t.Fatal("open returned a nil closer; callers defer it unconditionally")
	}
	closeFn()
}

// TestStreamClient_Open_ResumeHeaderPathUnreachable drives the same dial
// with a resume token set, proving the Last-Event-ID branch is taken and
// still classified rather than panicking on the header path.
func TestStreamClient_Open_ResumeHeaderPathUnreachable(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "no-daemon.sock")
	sc := newStreamClient(sock, UnixDialer)

	_, closeFn, err := sc.open(context.Background(), "job-1", "resume-token-1")
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("err = %v, want KindUnavailable", err)
	}
	closeFn()
}
