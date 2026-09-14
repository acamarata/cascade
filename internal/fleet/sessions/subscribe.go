package sessions

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
)

// Purpose (this file): the CLIENT half of this package's SSE stream — dial
//
//	the daemon's /events channel filtered to this topic, and fold the
//	text/event-stream field lines back into SessionRecord values.
//
// Inputs: a dialer for the daemon socket, and the socket path.
// Outputs: a channel of SessionRecord, closed when the stream ends.
// Constraints: the server half (sse.go) and this client are deliberately
//
//	neighbours. Before this existed the only consumer re-implemented the
//	dial and the SSE fold inside cmd/cascade, which meant the wire format
//	was defined in one package and parsed in another with nothing holding
//	the two together.
//
//	Decoding is tolerant by design: an undecodable data block is SKIPPED,
//	never fatal. The far end of this socket can write anything, and a
//	watcher that dies on one malformed block is worse than one that misses
//	it — the next block is very likely fine.
//
// SPORT: internal/fleet/sessions:subscribe (ADD) — P1-E16-W4-S34-T1.

// Topic is this package's stream topic on the EventsPath endpoint. It is
// changedNamespace's exported name: the server publishes to that namespace
// and this client filters on it, so they cannot drift apart.
const Topic = changedNamespace

// DialFunc opens a connection to the daemon socket.
type DialFunc func(ctx context.Context, socketPath string) (net.Conn, error)

// Stream is an open subscription to the session stream.
type Stream struct {
	// Records carries every decoded session change until the stream ends,
	// at which point it is closed.
	Records <-chan SessionRecord

	close func()
}

// Close releases the stream. It is safe to call more than once.
func (s *Stream) Close() {
	if s.close != nil {
		s.close()
	}
}

// Subscribe opens the session stream over the daemon socket.
//
// The caller must Close the returned Stream. Cancelling ctx also ends it —
// both are honoured, because a subscriber usually has a context and a
// supervisor usually has a handle, and neither should have to reach for the
// other's mechanism.
func Subscribe(ctx context.Context, dial DialFunc, socketPath string) (*Stream, error) {
	body, release, err := dialEvents(ctx, dial, socketPath)
	if err != nil {
		return nil, err
	}

	records := make(chan SessionRecord, 16)
	go func() {
		defer close(records)
		defer release()
		readRecords(ctx, body, records)
	}()

	return &Stream{Records: records, close: release}, nil
}

// dialEvents opens the filtered event stream.
func dialEvents(ctx context.Context, dial DialFunc, socketPath string) (io.ReadCloser, func(), error) {
	httpClient := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dial(ctx, socketPath)
		},
	}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"http://unix"+EventsPath+"?topic="+Topic, nil)
	if err != nil {
		return nil, func() {}, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, func() {}, err
	}
	return resp.Body, func() { _ = resp.Body.Close() }, nil
}

// readRecords folds the stream's field lines into records until it ends.
func readRecords(ctx context.Context, body io.Reader, out chan<- SessionRecord) {
	scanner := bufio.NewScanner(body)
	var data []string
	for scanner.Scan() {
		if ctx.Err() != nil {
			return
		}
		line := scanner.Text()
		if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
			continue
		}
		if line != "" || len(data) == 0 {
			continue
		}
		rec, ok := DecodeSessionEvent(strings.Join(data, "\n"))
		data = nil
		if !ok {
			continue
		}
		select {
		case out <- rec:
		case <-ctx.Done():
			return
		}
	}
}

// DecodeSessionEvent decodes one SSE "data:" block as a
// fleet.sessions.changed payload (Store.emit marshals a bare
// SessionRecord). ok is false for anything that is not one, which the
// caller skips rather than treating as fatal.
func DecodeSessionEvent(data string) (SessionRecord, bool) {
	var rec SessionRecord
	if err := json.Unmarshal([]byte(data), &rec); err != nil {
		return SessionRecord{}, false
	}
	return rec, true
}
