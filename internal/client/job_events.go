// Purpose: (*Client).JobEvents, the SDK half of `cascade ci watch`
//   (contract:ci-cli-attach): subscribes to the daemon's owner-only `ci`
//   GET /events topic and yields one job's checkpoint events as
//   JobEventFrame values.
// Inputs: a job id; the daemon socket the Client dials.
// Outputs: a frame channel and a closer. ID is the SSE resume token, Event
//   the event kind, Data the event's JSON payload. The stream replays the
//   stored history from the start, then sends one frame whose Event is
//   "ci.watch.replayed" (no ID, no Data), then follows live events: the
//   owner-only ci.watch_replay_end method, asked once the stream is open,
//   names the last stored sequence (any later event is live).
// Constraints: fail closed. A refused subscription (a principal caller,
//   HTTP 403) is KindPermissionDenied; any other non-stream answer is
//   KindUnavailable. A frame whose envelope or payload cannot be read ends
//   the stream rather than being skipped, so a consumer sees a disconnect,
//   never a gap; a daemon that names no replay end is refused. The
//   subscription has no client timeout: ctx and the closer bound it. This package never imports internal/rpc or internal/ci, so
//   the topic name, path and kinds are literals (as eventsPath is).
// SPORT: internal.client JobEvents (ADD, P1-CI-11).

package client

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"mime"
	"net/http"
	"net/url"

	"github.com/acamarata/cascade/pkg/cascade"
)

// jobEventsQuery selects the ci topic and its four checkpoint kinds.
var jobEventsQuery = url.Values{
	"topic":  {"ci"},
	"filter": {"ci.checkpoint.planned,ci.checkpoint.dispatched,ci.checkpoint.terminal,ci.checkpoint.aborted"},
}.Encode()

// replayFromStart is the resume token of sequence 0 (internal/rpc/sse.go
// formatResumeToken): the daemon replays every stored event after it.
const replayFromStart = "AAAAAAAAAAA"

// jobReplayedEvent marks the end of the replayed history; internal/ci
// watch_cmd.go holds the same literal.
const jobReplayedEvent = "ci.watch.replayed"

// JobEventFrame is one event of a job's CI stream.
type JobEventFrame struct {
	ID, Event, Data string
}

// sseEnvelope is the daemon's data line (internal/rpc/sse.go sseEventJSON);
// Payload arrives base64 encoded, which []byte decodes.
type sseEnvelope struct {
	Kind    string `json:"kind"`
	Payload []byte `json:"payload"`
}

// JobEvents subscribes to the ci topic and streams jobID's frames until
// ctx ends, the closer runs, the daemon disconnects or a frame cannot be
// read; each closes the channel.
func (c *Client) JobEvents(ctx context.Context, jobID string) (<-chan JobEventFrame, func(), error) {
	if jobID == "" {
		return nil, func() {}, cascade.New(cascade.KindInvalidInput, "client: job events need a job id")
	}
	ctx, cancel := context.WithCancel(ctx)
	resp, err := c.openJobEvents(ctx)
	if err != nil {
		cancel()
		return nil, func() {}, err
	}
	var boundary struct {
		End *uint64 `json:"end"`
	}
	if err := c.Do(ctx, "ci.watch_replay_end", nil, &boundary); err != nil {
		_ = resp.Body.Close()
		cancel()
		return nil, func() {}, withDaemonHint(err)
	}
	if boundary.End == nil {
		_ = resp.Body.Close()
		cancel()
		return nil, func() {}, cascade.New(cascade.KindUnavailable, "client: the daemon named no ci replay end")
	}
	end := *boundary.End
	sse, closeBody := startStream(ctx, resp.Body)
	closer := func() { cancel(); closeBody() }
	out := make(chan JobEventFrame)
	go func() {
		defer close(out)
		defer closer()
		forwardJobFrames(ctx, sse, out, jobID, end)
	}()
	return out, closer, nil
}

// withDaemonHint names `cascade daemon run` on an unavailable daemon.
func withDaemonHint(err error) error {
	if kind, _ := cascade.KindOf(err); kind == cascade.KindUnavailable {
		return cascade.Wrap(cascade.KindUnavailable, err, "client: no daemon event stream; start `cascade daemon run`")
	}
	return err
}

// openJobEvents issues the subscription and checks it is an event stream.
func (c *Client) openJobEvents(ctx context.Context) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, unixBaseURL+eventsPath+"?"+jobEventsQuery, nil)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInternal, err, "client: build job events request")
	}
	req.Header.Set("Last-Event-ID", replayFromStart)
	hc := *c.httpClient
	hc.Timeout = 0
	resp, err := hc.Do(req)
	if err != nil {
		return nil, withDaemonHint(classifyTransportError(ctx, "job events", c.target, err))
	}
	media, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if resp.StatusCode == http.StatusOK && media == "text/event-stream" {
		return resp, nil
	}
	_ = resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden {
		return nil, cascade.New(cascade.KindPermissionDenied, "client: the daemon's ci event topic is owner-only")
	}
	return nil, cascade.Newf(cascade.KindUnavailable,
		"client: the daemon refused the ci event topic (HTTP %d); start `cascade daemon run`", resp.StatusCode)
}

// forwardJobFrames decodes each SSE event and forwards jobID's frames,
// then the replay marker once the event at sequence end has passed (at
// once when end is 0). It returns on the first unreadable frame.
func forwardJobFrames(ctx context.Context, in <-chan sseEvent, out chan<- JobEventFrame, jobID string, end uint64) {
	replayed := end == 0
	if replayed && !send(ctx, out, JobEventFrame{Event: jobReplayedEvent}) {
		return
	}
	for {
		var ev sseEvent
		var open bool
		select {
		case <-ctx.Done():
			return
		case ev, open = <-in:
		}
		if !open {
			return
		}
		frame, mine, ok := decodeJobFrame(ev, jobID)
		seq, seqOK := eventSeq(ev.ID)
		if !ok || !seqOK {
			return
		}
		if mine && !send(ctx, out, frame) {
			return
		}
		if !replayed && seq >= end {
			replayed = true
			if !send(ctx, out, JobEventFrame{Event: jobReplayedEvent}) {
				return
			}
		}
	}
}

// send delivers f unless ctx ends first.
func send(ctx context.Context, out chan<- JobEventFrame, f JobEventFrame) bool {
	select {
	case out <- f:
		return true
	case <-ctx.Done():
		return false
	}
}

// eventSeq decodes an SSE id (internal/rpc/sse.go's base64url resume
// token of the event's sequence).
func eventSeq(id string) (uint64, bool) {
	b, err := base64.RawURLEncoding.DecodeString(id)
	if err != nil || len(b) != 8 {
		return 0, false
	}
	return binary.BigEndian.Uint64(b), true
}

// decodeJobFrame decodes ev. mine reports whether it belongs to jobID; ok
// is false when the envelope or payload cannot be read.
func decodeJobFrame(ev sseEvent, jobID string) (frame JobEventFrame, mine, ok bool) {
	var env sseEnvelope
	if json.Unmarshal([]byte(ev.Data), &env) != nil || env.Kind == "" {
		return JobEventFrame{}, false, false
	}
	var payload struct {
		JobID string `json:"job_id"`
	}
	if json.Unmarshal(env.Payload, &payload) != nil {
		return JobEventFrame{}, false, false
	}
	return JobEventFrame{ID: ev.ID, Event: env.Kind, Data: string(env.Payload)}, payload.JobID == jobID, true
}
