package client

// Purpose: the SSE half of the IPC client SDK — a context-cancellable
//   reader for the daemon's GET /events channel (internal/rpc.SSEHandler
//   is the server side this dials), plus feedSSELine, FuzzSSEEventParse's
//   target (fuzz_test.go): the untrusted-input parser for the
//   text/event-stream "id"/"data" field lines the daemon (or anything on
//   the far end of the socket) writes.
//
// Scope note (contract/tree note, see status.go's and this ticket's
//   completion report): this SDK's whole exported surface today is
//   Client/Do/Status, because Status is the one D/S-06.T3-registered
//   method with an in-scope production caller (cmd/cascade/status.go).
//   No cmd/cascade command in this ticket's files_scope subscribes to
//   GET /events (there is no "cascade events"/"--follow" verb to wire),
//   so streamClient and its supporting types stay UNEXPORTED: an exported
//   type reachable only from this package's own tests would trip
//   internal/build's test-only-usage gate (R-14.175), whose allow list
//   (internal/build/testonly-allow.json) is explicitly outside this
//   ticket's files_scope to edit. Every piece here is fully implemented
//   and fully tested against a real internal/rpc.SSEHandler over a real
//   unix socket (stream_integration_test.go) — exporting it is a
//   mechanical, one-line capitalization once a consuming command exists.
//
// Constraints: feedSSELine must never panic on arbitrary input
//   (06-FORGE-SPEC §5 rule 7); seed corpus at
//   internal/testdata/fuzz/FuzzSSEEventParse/ with a provenance README.

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"strings"
)

// eventsPath mirrors internal/rpc.EventsPath ("/events"). Duplicated as a
// literal for the same reason as client.go's rpcPath: this package never
// imports internal/rpc.
const eventsPath = "/events"

// sseEvent is one decoded SSE record: the resume-token id
// (internal/rpc/sse.go's opaque base64url Seq encoding) and the event's
// data payload, joined across every "data:" line the block carried (per
// the SSE spec, multiple "data:" lines in one block join with "\n").
type sseEvent struct {
	ID   string
	Data string
}

// sseAccumulator folds a text/event-stream's field lines ("id: ...",
// "data: ...", ": comment") into complete sseEvent records. It holds no
// I/O state — feed is a pure function of one line plus the accumulator's
// prior state — so it is exactly what FuzzSSEEventParse drives directly,
// and what streamClient's real read loop drives one network line at a
// time.
type sseAccumulator struct {
	id   string
	data []string
}

// feed processes one line of an SSE stream (no trailing newline) and
// reports the completed event plus complete=true when line was the blank
// line terminating a block (the SSE spec's "dispatch the event" trigger).
// feed NEVER panics: every line, however malformed (no colon, a bare
// colon, non-UTF8-looking bytes smuggled into a Go string, an
// unrecognized field name), is either folded into accumulator state or
// silently ignored, matching the same permissive-parser shape
// internal/rpc/sse.go's own parseResumeToken uses for the companion
// Last-Event-ID header (never error, only "recognized" or "not").
func (a *sseAccumulator) feed(line string) (ev sseEvent, complete bool) {
	if line == "" {
		ev = sseEvent{ID: a.id, Data: strings.Join(a.data, "\n")}
		a.id = ""
		a.data = nil
		return ev, true
	}
	if strings.HasPrefix(line, ":") {
		// A comment/heartbeat line (internal/rpc/sse.go writes
		// ": keep-alive"): carries no field, never accumulated.
		return sseEvent{}, false
	}
	field, value := splitSSEField(line)
	switch field {
	case "id":
		a.id = value
	case "data":
		a.data = append(a.data, value)
	default:
		// Any other field name (e.g. a future "event:" or "retry:") is
		// recognized SSE grammar this server never emits today; ignored
		// rather than rejected, per the SSE spec's own forward-
		// compatibility rule for unknown fields.
	}
	return sseEvent{}, false
}

// splitSSEField splits one non-empty, non-comment SSE line into its field
// name and value, per the spec: the value is everything after the first
// ':', with at most one leading space stripped. A line with no ':' at all
// is itself the field name with an empty value (the spec's own rule for a
// bare field line).
func splitSSEField(line string) (field, value string) {
	idx := strings.IndexByte(line, ':')
	if idx < 0 {
		return line, ""
	}
	field = line[:idx]
	value = strings.TrimPrefix(line[idx+1:], " ")
	return field, value
}

// streamClient dials the daemon's GET /events SSE channel. Unexported —
// see this file's Scope note.
type streamClient struct {
	socketPath string
	httpClient *http.Client
}

// newStreamClient builds a streamClient dialing socketPath through dial.
// Shares client.go's newHTTPClient so both halves of this SDK reach the
// socket the same way. No client-level timeout: an SSE subscription is
// long-lived by definition, so its lifetime is bounded by the caller's
// context, never by a fixed deadline.
func newStreamClient(socketPath string, dial DialFunc) *streamClient {
	return &streamClient{socketPath: socketPath, httpClient: newHTTPClient(socketPath, dial, 0)}
}

// eventsURL builds the GET /events request URL, filtered to jobID when
// one is given (empty means subscribe-all). Pure, so the filter's wire
// shape is asserted without a socket.
func eventsURL(jobID string) string {
	url := unixBaseURL + eventsPath
	if jobID != "" {
		url += "?job_id=" + jobID
	}
	return url
}

// open dials GET /events (optionally filtered to jobID, empty for
// subscribe-all, optionally resuming after lastEventID) and streams
// decoded events on the returned channel until ctx is canceled, the
// connection closes, or a read error occurs — any of which closes the
// channel. The returned func closes the underlying connection
// immediately; callers should defer it even though canceling ctx also
// unblocks the read loop, matching net/http's own cancel-via-context
// contract.
func (s *streamClient) open(ctx context.Context, jobID, lastEventID string) (<-chan sseEvent, func(), error) {
	body, err := s.dialEvents(ctx, jobID, lastEventID)
	if err != nil {
		return nil, func() {}, err
	}
	events, closeFn := startStream(ctx, body)
	return events, closeFn, nil
}

// dialEvents performs the one socket-bound step of open: issue the GET and
// hand back the still-open response body. lastEventID, when set, becomes
// the SSE Last-Event-ID header the daemon's resume-token path reads.
func (s *streamClient) dialEvents(ctx context.Context, jobID, lastEventID string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, eventsURL(jobID), nil)
	if err != nil {
		return nil, classifyTransportError(ctx, "events", s.socketPath, err)
	}
	if lastEventID != "" {
		req.Header.Set("Last-Event-ID", lastEventID)
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, classifyTransportError(ctx, "events", s.socketPath, err)
	}
	return resp.Body, nil
}

// startStream spawns the read loop over an already-open event-stream body
// and returns the event channel plus the closer that tears the body down.
// Pure of any transport concern, so the whole framing/heartbeat/
// disconnect contract is asserted against a scripted body in the
// no-network unit lane.
func startStream(ctx context.Context, body io.ReadCloser) (<-chan sseEvent, func()) {
	events := make(chan sseEvent)
	go streamLoop(ctx, body, events)
	return events, func() { _ = body.Close() }
}

// streamLoop reads body line by line, folding lines through an
// sseAccumulator and forwarding each completed event on out, until ctx is
// canceled or the reader returns an error/EOF. It always closes out
// before returning, so a range over the returned channel terminates
// cleanly: a mid-stream disconnect ends the range rather than delivering
// the half-read block the peer never terminated.
func streamLoop(ctx context.Context, body io.Reader, out chan<- sseEvent) {
	defer close(out)
	scanner := bufio.NewScanner(body)
	var acc sseAccumulator
	for scanner.Scan() {
		if ctx.Err() != nil {
			return
		}
		ev, complete := acc.feed(scanner.Text())
		if !complete {
			continue
		}
		if ev.ID == "" && ev.Data == "" {
			continue // a spurious blank block (e.g. two blank lines in a row)
		}
		select {
		case out <- ev:
		case <-ctx.Done():
			return
		}
	}
}
