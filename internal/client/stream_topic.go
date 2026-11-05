package client

import (
	"context"
	"io"
	"net/http"
	"net/url"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Event is one decoded SSE record from the daemon's GET /events channel.
// Err is set only on the final event of a stream the daemon closed; it is a
// KindUnavailable error. A stream ended by ctx cancel carries no Err.
type Event struct {
	ID   string
	Data string
	Err  error
}

// StreamTopic opens a GET /events SSE subscription filtered to topic with
// additional query parameters, yielding decoded events until ctx is
// canceled, the connection closes, or a read error occurs. The caller's
// principal (token from environment or owner-CLI marker) is bound through
// the Client's transport.
//
// The returned channel closes when the stream ends. The returned func
// closes the underlying connection immediately; defer it even though
// canceling ctx also unblocks the read loop.
func (c *Client) StreamTopic(ctx context.Context, topic string, query url.Values) (<-chan Event, func(), error) {
	body, err := c.dialTopic(ctx, topic, query)
	if err != nil {
		return nil, func() {}, err
	}
	out, closeFn := openTopicStream(ctx, body)
	return out, closeFn, nil
}

// openTopicStream starts the shared SSE reader on body and forwards its
// events as topic Events on the returned channel.
func openTopicStream(ctx context.Context, body io.ReadCloser) (<-chan Event, func()) {
	inner, closeFn := startStream(ctx, body)
	out := make(chan Event)
	go forwardTopic(ctx, inner, out)
	return out, closeFn
}

// forwardTopic copies inner events to out. When inner closes without ctx
// being canceled, the daemon closed the stream: one final Event carrying a
// KindUnavailable error is sent. It always closes out.
func forwardTopic(ctx context.Context, inner <-chan sseEvent, out chan<- Event) {
	defer close(out)
	for ev := range inner {
		select {
		case out <- Event{ID: ev.ID, Data: ev.Data}:
		case <-ctx.Done():
			return
		}
	}
	if ctx.Err() != nil {
		return
	}
	end := Event{Err: cascade.Wrap(cascade.KindUnavailable, io.ErrUnexpectedEOF, "client: topic stream closed by daemon")}
	select {
	case out <- end:
	case <-ctx.Done():
	}
}

// topicURL builds the GET /events URL for topic plus query. The topic
// argument always wins over a "topic" key in query.
func topicURL(topic string, query url.Values) string {
	q := make(url.Values)
	for k, vs := range query {
		q[k] = vs
	}
	q.Set("topic", topic) // the topic argument always wins over a query key
	return unixBaseURL + eventsPath + "?" + q.Encode()
}

// checkTopicResponse closes body and returns a KindUnavailable error when
// status is outside 2xx; otherwise it leaves body open and returns nil.
func checkTopicResponse(status int, body io.Closer, target string) error {
	if status >= 200 && status <= 299 {
		return nil
	}
	_ = body.Close()
	return cascade.Newf(cascade.KindUnavailable, "client: topic stream rejected by daemon at %s: HTTP %d", target, status)
}

// newTopicRequest builds the GET /events request for topic plus query.
func newTopicRequest(ctx context.Context, topic string, query url.Values) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, topicURL(topic, query), nil)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInternal, err, "client: build topic request")
	}
	return req, nil
}

// acceptTopicBody returns body when status is 2xx, else closes it and
// returns the KindUnavailable rejection.
func acceptTopicBody(status int, body io.ReadCloser, target string) (io.ReadCloser, error) {
	if err := checkTopicResponse(status, body, target); err != nil {
		return nil, err
	}
	return body, nil
}

// dialTopic issues GET /events?topic=<topic>&<query> through the client's
// transport and returns the still-open response body. Any transport
// failure is classified into the taxonomy. Like JobEvents, the stream has
// no client timeout: ctx and the closer bound it.
func (c *Client) dialTopic(ctx context.Context, topic string, query url.Values) (io.ReadCloser, error) {
	req, err := newTopicRequest(ctx, topic, query)
	if err != nil {
		return nil, err
	}
	hc := *c.httpClient
	hc.Timeout = 0
	resp, err := hc.Do(req)
	if err != nil {
		return nil, classifyTransportError(ctx, "topic", c.target, err)
	}
	return acceptTopicBody(resp.StatusCode, resp.Body, c.target)
}
