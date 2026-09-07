// Purpose: the streaming leg of the anthropic driver (P1-E10-W3-S19-T2):
//   ModelProvider.Stream over the Messages API's SSE body, plus the shared
//   HTTP/error-mapping plumbing Chat and Count (anthropic.go) also call.
// Inputs/Outputs: typed provider.StreamEvent values delivered to the sink,
//   in order, terminating in exactly one done or error event (R-21.217).
// Constraints: the SSE decode path is untrusted network input and is
//   fuzzed by FuzzAnthropicWireDecode (anthropic_test.go) against
//   decodeSSEEvent - it must never panic, however malformed the input.
// SPORT: placeholder: providers/anthropic driver (ADD) - see anthropic.go.

package anthropic

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// maxReadBody bounds how much of a non-streaming response this driver will
// buffer, so a misbehaving or hostile endpoint cannot exhaust memory.
const maxReadBody = 1 << 20 // 1 MiB

// doJSON issues one JSON request/response exchange against path, retrying
// exactly once on a 401 after forcing an OAuth refresh.
func (d *Driver) doJSON(ctx context.Context, path string, body, out any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return cascade.Wrap(cascade.KindInvalidInput, err, "anthropic: encoding request body")
	}
	resp, err := d.roundTrip(ctx, path, payload)
	if err != nil {
		return err
	}
	if resp.status < 200 || resp.status >= 300 {
		return mapStatusError(resp.status, resp.body)
	}
	if err := json.Unmarshal(resp.body, out); err != nil {
		return cascade.Wrap(cascade.KindIntegrity, err, "anthropic: decoding response body")
	}
	return nil
}

// rawResponse is the trimmed-down result roundTrip/send hand back.
type rawResponse struct {
	status int
	body   []byte
}

// HTTP status codes named here rather than imported from net/http, so this
// file (which decodeSSEEvent's fuzz target also lives in) needs no
// "net"/"net/http" import (Art.7.2).
const (
	statusBadRequest          = 400
	statusUnauthorized        = 401
	statusForbidden           = 403
	statusNotFound            = 404
	statusRequestTooLarge     = 413
	statusTooManyRequests     = 429
	statusOverloaded          = 529 // Anthropic's overloaded_error status, not IANA-registered.
	statusInternalServerError = 500
)

// roundTrip sends payload once, retrying a single time on a 401 after
// forcing an OAuth refresh (key-mode auth never retries: a bad key does not
// become good by resending it).
func (d *Driver) roundTrip(ctx context.Context, path string, payload []byte) (rawResponse, error) {
	resp, err := d.send(ctx, path, payload)
	if err != nil {
		return rawResponse{}, err
	}
	if resp.status == statusUnauthorized && d.cfg.Auth.Mode == AuthModeOAuth {
		if _, rerr := d.forceRefresh(ctx); rerr != nil {
			return rawResponse{}, rerr
		}
		return d.send(ctx, path, payload)
	}
	return resp, nil
}

// send performs one HTTP round trip, mapping ctx and network failures onto
// the taxonomy before the caller ever sees a raw error.
func (d *Driver) send(ctx context.Context, path string, payload []byte) (rawResponse, error) {
	req, err := d.newRequest(ctx, path, payload)
	if err != nil {
		return rawResponse{}, err
	}
	resp, err := d.cfg.Doer.Do(ctx, req)
	if err != nil {
		return rawResponse{}, mapTransportError(ctx, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxReadBody))
	if err != nil {
		return rawResponse{}, cascade.Wrap(cascade.KindUnavailable, err, "anthropic: reading response body")
	}
	return rawResponse{status: resp.Status, body: body}, nil
}

// newRequest builds one outbound HTTPRequest, resolving its auth header.
func (d *Driver) newRequest(ctx context.Context, path string, payload []byte) (HTTPRequest, error) {
	name, value, err := d.headerFor(ctx)
	if err != nil {
		return HTTPRequest{}, err
	}
	return HTTPRequest{
		Method: "POST",
		URL:    d.cfg.baseURL() + path,
		Headers: map[string]string{
			"content-type":      "application/json",
			"anthropic-version": anthropicVersion,
			name:                value,
		},
		Body: payload,
	}, nil
}

// mapStatusError classifies an HTTP error response by status code, per
// 06 §5's explicit-mapping requirement (429 and the 529 overload status
// named specifically).
func mapStatusError(status int, body []byte) error {
	msg := errorMessage(status, body)
	switch status {
	case statusBadRequest, statusRequestTooLarge:
		return cascade.New(cascade.KindInvalidInput, msg)
	case statusUnauthorized, statusForbidden:
		return cascade.New(cascade.KindPermissionDenied, msg)
	case statusNotFound:
		return cascade.New(cascade.KindNotFound, msg)
	case statusTooManyRequests:
		return cascade.New(cascade.KindQuotaExhausted, msg)
	case statusOverloaded:
		return cascade.New(cascade.KindUnavailable, msg)
	default:
		if status >= statusInternalServerError {
			return cascade.New(cascade.KindUnavailable, msg)
		}
		return cascade.New(cascade.KindInternal, msg)
	}
}

// wireSSEEvent is one decoded Messages-API SSE event: the "event:" line's
// name and the JSON payload from its "data:" line(s).
type wireSSEEvent struct {
	Name string
	Data []byte
}

// wireDeltaPayload is a content_block_delta event's data payload.
type wireDeltaPayload struct {
	Delta struct {
		Text string `json:"text"`
	} `json:"delta"`
}

// wireMessageDeltaPayload is a message_delta event's data payload, carrying
// the running usage total.
type wireMessageDeltaPayload struct {
	Usage wireUsage `json:"usage"`
}

// decodeSSEEvent turns one wireSSEEvent into a provider.StreamEvent, or
// (zero value, false, nil) for an event kind this driver passes over
// silently (message_start, content_block_start/stop, ping). A malformed
// "data:" payload is reported as a KindIntegrity error, never a panic -
// this is the function FuzzAnthropicWireDecode drives directly.
func decodeSSEEvent(ev wireSSEEvent) (provider.StreamEvent, bool, error) {
	switch ev.Name {
	case "content_block_delta":
		var payload wireDeltaPayload
		if err := json.Unmarshal(ev.Data, &payload); err != nil {
			return provider.StreamEvent{}, false, cascade.Wrap(cascade.KindIntegrity, err, "anthropic: decoding stream delta")
		}
		return provider.StreamEvent{Kind: provider.StreamEventDelta, Delta: payload.Delta.Text}, true, nil
	case "message_delta":
		var payload wireMessageDeltaPayload
		if err := json.Unmarshal(ev.Data, &payload); err != nil {
			return provider.StreamEvent{}, false, cascade.Wrap(cascade.KindIntegrity, err, "anthropic: decoding stream usage")
		}
		usage := provider.Usage{InputTokens: payload.Usage.InputTokens, OutputTokens: payload.Usage.OutputTokens}
		return provider.StreamEvent{Kind: provider.StreamEventUsage, Usage: usage}, true, nil
	case "message_stop":
		return provider.StreamEvent{Kind: provider.StreamEventDone}, true, nil
	case "error":
		var wireErr wireErrorBody
		if err := json.Unmarshal(ev.Data, &wireErr); err != nil {
			return provider.StreamEvent{}, false, cascade.Wrap(cascade.KindIntegrity, err, "anthropic: decoding stream error event")
		}
		streamErr := cascade.Newf(cascade.KindUnavailable, "anthropic: stream error %s: %s", wireErr.Error.Type, wireErr.Error.Message)
		return provider.StreamEvent{Kind: provider.StreamEventError, Err: streamErr}, true, nil
	default:
		// message_start, content_block_start, content_block_stop, ping,
		// and any future event name this driver does not yet interpret.
		return provider.StreamEvent{}, false, nil
	}
}

// scanSSEEvents reads r line by line and returns each complete event as a
// wireSSEEvent, in order. It is deliberately tolerant of a stream that ends
// mid-event (no trailing blank line): whatever was accumulated is returned
// rather than discarded, so a truncated connection still surfaces partial
// progress to decodeSSEEvent's error path instead of silently vanishing.
func scanSSEEvents(r io.Reader) []wireSSEEvent {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), maxReadBody)
	var out []wireSSEEvent
	var name string
	var data bytes.Buffer
	flush := func() {
		if name != "" || data.Len() > 0 {
			// data.Bytes() aliases the buffer's backing array, which Reset
			// below reuses next - clone or a later event corrupts this one.
			out = append(out, wireSSEEvent{Name: name, Data: bytes.Clone(bytes.TrimSuffix(data.Bytes(), []byte("\n")))})
		}
		name = ""
		data.Reset()
	}
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, "event:"):
			name = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
			data.WriteByte('\n')
		}
	}
	flush()
	return out
}

// Stream implements provider.ModelProvider.Stream.
func (d *Driver) Stream(ctx context.Context, req provider.ChatRequest, sink provider.StreamSink) error {
	system, turns, err := buildMessages(req.Messages)
	if err != nil {
		return err
	}
	maxTokens := req.MaxOutputTokens
	if maxTokens <= 0 {
		maxTokens = defaultMaxOutputTokens
	}
	payload, err := json.Marshal(wireChatRequest{
		Model:     d.cfg.model(req.Model),
		MaxTokens: maxTokens,
		System:    system,
		Messages:  turns,
		Stream:    true,
	})
	if err != nil {
		return cascade.Wrap(cascade.KindInvalidInput, err, "anthropic: encoding stream request body")
	}

	httpReq, err := d.newRequest(ctx, "/v1/messages", payload)
	if err != nil {
		return err
	}
	resp, err := d.cfg.Doer.Do(ctx, httpReq)
	if err != nil {
		return mapTransportError(ctx, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.Status < 200 || resp.Status >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxReadBody))
		return mapStatusError(resp.Status, body)
	}
	return d.emit(scanSSEEvents(resp.Body), sink)
}

// emit decodes each raw SSE event and delivers it to sink, guaranteeing
// exactly one terminal (done or error) delivery per R-21.217.
func (d *Driver) emit(raw []wireSSEEvent, sink provider.StreamSink) error {
	for _, ev := range raw {
		event, ok, err := decodeSSEEvent(ev)
		if err != nil {
			_ = sink(provider.StreamEvent{Kind: provider.StreamEventError, Err: err})
			return err
		}
		if !ok {
			continue
		}
		if serr := sink(event); serr != nil {
			return serr
		}
		if event.Kind == provider.StreamEventDone || event.Kind == provider.StreamEventError {
			if event.Kind == provider.StreamEventError {
				return event.Err
			}
			return nil
		}
	}
	return sink(provider.StreamEvent{Kind: provider.StreamEventDone})
}
