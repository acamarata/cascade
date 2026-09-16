package sessions

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
)

// Purpose (this file): the CLIENT half of this package's SSE stream — fold
//
//	the text/event-stream field lines the server (sse.go) writes back into
//	SessionRecord values.
//
// Inputs: any io.Reader carrying the event stream.
// Outputs: SessionRecord values on the caller's channel.
// Constraints: the server half (sse.go) and this fold are deliberately
//
//	neighbours. The wire format must be defined and parsed in one package,
//	or the two drift; TestTopicMatchesTheServersNamespace holds the topic
//	to the same rule.
//
//	The DIAL is not here. Opening the socket belongs at the composition
//	root (cmd/cascade), which already had to dial /events for
//	`cascade fleet sessions --watch`; a second transport in this package
//	meant two hand-rolled clients for one endpoint. This file takes an
//	io.Reader instead, which is also what makes the fold testable in the
//	default unit lane — Art.7.2 keeps network calls out of it.
//	See journals/RULING-coverage-socket-code.md.
//
//	Decoding is tolerant by design: an undecodable data block is SKIPPED,
//	never fatal. The far end of this socket can write anything, and a
//	watcher that dies on one malformed block is worse than one that misses
//	it — the next block is very likely fine.
//
// SPORT: internal/fleet/sessions:stream-fold (ADD) — P1-E16-W4-S34-T1.

// Topic is this package's stream topic on the EventsPath endpoint. It is
// changedNamespace's exported name: the server publishes to that namespace
// and this client filters on it, so they cannot drift apart.
const Topic = changedNamespace

// ReadRecords folds the stream's field lines into records until it ends.
//
// It returns when body is exhausted or ctx is cancelled; it does NOT close
// out, because the caller owns that channel and usually has a defer for it
// already.
func ReadRecords(ctx context.Context, body io.Reader, out chan<- SessionRecord) {
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
