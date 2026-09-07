// Purpose: the streaming leg of the ollama driver (P1-E10-W3-S19-T5):
//   ModelProvider.Stream over Ollama's NDJSON (newline-delimited JSON, not
//   SSE) chat body, plus the shared HTTP/error-mapping plumbing Chat, Embed
//   and Capabilities (ollama.go) also call.
// Inputs/Outputs: typed provider.StreamEvent values delivered to the sink,
//   in order, terminating in exactly one done or error event (R-21.217).
// Constraints: the NDJSON decode path is untrusted network input and is
//   fuzzed by FuzzOllamaWireDecode (ollama_test.go) against
//   decodeStreamChunk - it must never panic, however malformed the input.
//   A stream that ends before a done chunk arrives (truncation) surfaces as
//   a typed KindIntegrity error, never a silent short read.
// SPORT: placeholder: providers/ollama driver (ADD) - see ollama.go.

package ollama

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// maxReadBody bounds how much of a response this driver will buffer, so a
// misbehaving or hostile endpoint cannot exhaust memory.
const maxReadBody = 1 << 20 // 1 MiB

// rawResponse is the trimmed-down result send hands back.
type rawResponse struct {
	status int
	body   []byte
}

// HTTP status codes named here rather than imported from net/http, so this
// file (which decodeStreamChunk's fuzz target also lives alongside) needs
// no "net"/"net/http" import (Art.7.2).
const (
	statusBadRequest          = 400
	statusUnauthorized        = 401
	statusForbidden           = 403
	statusNotFound            = 404
	statusInternalServerError = 500
)

// authHeader resolves the optional Bearer-token header, or (empty, empty,
// false, nil) when no token is configured - the common local-deployment
// case.
func (d *Driver) authHeader(ctx context.Context) (name, value string, ok bool, err error) {
	if strings.TrimSpace(d.cfg.TokenRef) == "" {
		return "", "", false, nil
	}
	token, rerr := d.cfg.Resolver.Resolve(ctx, d.cfg.TokenRef)
	if rerr != nil {
		return "", "", false, rerr
	}
	return "authorization", "Bearer " + token, true, nil
}

// newRequest builds one outbound HTTPRequest, resolving its optional auth
// header. A nil payload builds a GET request; otherwise POST.
func (d *Driver) newRequest(ctx context.Context, path string, payload []byte) (HTTPRequest, error) {
	headers := map[string]string{"content-type": "application/json"}
	name, value, ok, err := d.authHeader(ctx)
	if err != nil {
		return HTTPRequest{}, err
	}
	if ok {
		headers[name] = value
	}
	method := "POST"
	if payload == nil {
		method = "GET"
	}
	return HTTPRequest{Method: method, URL: d.cfg.baseURL() + path, Headers: headers, Body: payload}, nil
}

// send performs one HTTP round trip, mapping ctx and transport failures
// onto the taxonomy before the caller ever sees a raw error.
func (d *Driver) send(ctx context.Context, path string, payload []byte) (rawResponse, error) {
	req, err := d.newRequest(ctx, path, payload)
	if err != nil {
		return rawResponse{}, err
	}
	resp, err := d.cfg.Doer.Do(ctx, req)
	if err != nil {
		return rawResponse{}, mapTransportError(ctx, err, d.cfg.baseURL())
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxReadBody))
	if err != nil {
		return rawResponse{}, cascade.Wrap(cascade.KindUnavailable, err, "ollama: reading response body")
	}
	return rawResponse{status: resp.Status, body: body}, nil
}

// doJSON issues one JSON POST request/response exchange against path.
func (d *Driver) doJSON(ctx context.Context, path string, body, out any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return cascade.Wrap(cascade.KindInvalidInput, err, "ollama: encoding request body")
	}
	resp, err := d.send(ctx, path, payload)
	if err != nil {
		return err
	}
	if resp.status < 200 || resp.status >= 300 {
		return mapStatusError(resp.status, resp.body)
	}
	if err := json.Unmarshal(resp.body, out); err != nil {
		return cascade.Wrap(cascade.KindIntegrity, err, "ollama: decoding response body")
	}
	return nil
}

// doGET issues one GET request/response exchange against path.
func (d *Driver) doGET(ctx context.Context, path string, out any) error {
	resp, err := d.send(ctx, path, nil)
	if err != nil {
		return err
	}
	if resp.status < 200 || resp.status >= 300 {
		return mapStatusError(resp.status, resp.body)
	}
	if err := json.Unmarshal(resp.body, out); err != nil {
		return cascade.Wrap(cascade.KindIntegrity, err, "ollama: decoding response body")
	}
	return nil
}

// mapTransportError classifies a network-layer failure: context
// cancellation/deadline first (they are the caller's own signal, not the
// server's); everything else - most commonly nothing listening on
// baseURL, since Ollama is a local server - maps to KindUnavailable with a
// message naming the URL, never a bare dial error.
func mapTransportError(ctx context.Context, err error, baseURL string) error {
	if errors.Is(ctx.Err(), context.Canceled) {
		return cascade.Wrap(cascade.KindCanceled, err, "ollama: request canceled")
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return cascade.Wrap(cascade.KindTimeout, err, "ollama: request deadline exceeded")
	}
	return cascade.Wrapf(cascade.KindUnavailable, err,
		"ollama: could not reach %s - is the ollama server running?", baseURL)
}

// wireErrorBody is Ollama's documented error envelope: {"error": "..."}.
type wireErrorBody struct {
	Error string `json:"error"`
}

// errorMessage extracts the vendor's error message from body, falling back
// to a generic message when the body is not the documented error envelope.
func errorMessage(status int, body []byte) string {
	var wireErr wireErrorBody
	if err := json.Unmarshal(body, &wireErr); err == nil && wireErr.Error != "" {
		return fmt.Sprintf("ollama: http %d: %s", status, wireErr.Error)
	}
	return fmt.Sprintf("ollama: http %d", status)
}

// mapStatusError classifies an HTTP error response by status code. 404 is
// Ollama's unknown-model signal (e.g. a model never pulled).
func mapStatusError(status int, body []byte) error {
	msg := errorMessage(status, body)
	switch status {
	case statusBadRequest:
		return cascade.New(cascade.KindInvalidInput, msg)
	case statusUnauthorized, statusForbidden:
		return cascade.New(cascade.KindPermissionDenied, msg)
	case statusNotFound:
		return cascade.New(cascade.KindNotFound, msg)
	default:
		if status >= statusInternalServerError {
			return cascade.New(cascade.KindUnavailable, msg)
		}
		return cascade.New(cascade.KindInternal, msg)
	}
}

// wireStreamChunk is one NDJSON line of Ollama's streaming /api/chat body -
// each line a complete, independently-decodable JSON object, not an SSE
// "event:"/"data:" pair.
type wireStreamChunk struct {
	Message         wireMessage `json:"message"`
	Done            bool        `json:"done"`
	DoneReason      string      `json:"done_reason"`
	PromptEvalCount int         `json:"prompt_eval_count"`
	EvalCount       int         `json:"eval_count"`
	Error           string      `json:"error"`
}

// streamChunk is decodeStreamChunk's decoded result for one NDJSON line.
type streamChunk struct {
	delta    string
	done     bool
	usage    provider.Usage
	errEvent error
}

// decodeStreamChunk turns one raw NDJSON line into a streamChunk, or a
// KindIntegrity error for a line that is not valid JSON. It must never
// panic, however malformed or truncated line is - this is the function
// FuzzOllamaWireDecode drives directly. A line carrying Ollama's own
// {"error": "..."} shape (a mid-stream failure Ollama itself reports, e.g.
// an aborted generation) decodes successfully but reports errEvent rather
// than a decode error, since the JSON itself was well-formed.
func decodeStreamChunk(line []byte) (streamChunk, error) {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 {
		return streamChunk{}, nil
	}
	var wire wireStreamChunk
	if err := json.Unmarshal(trimmed, &wire); err != nil {
		return streamChunk{}, cascade.Wrap(cascade.KindIntegrity, err, "ollama: decoding stream chunk")
	}
	if wire.Error != "" {
		return streamChunk{errEvent: cascade.Newf(cascade.KindUnavailable, "ollama: stream error: %s", wire.Error)}, nil
	}
	return streamChunk{
		delta: wire.Message.Content,
		done:  wire.Done,
		usage: provider.Usage{InputTokens: wire.PromptEvalCount, OutputTokens: wire.EvalCount},
	}, nil
}

// emitStream reads NDJSON lines from scanner and delivers decoded events to
// sink, guaranteeing exactly one terminal (done or error) delivery per
// R-21.217 - including when the stream ends without ever sending a done
// chunk (truncation), which is reported as a typed KindIntegrity error
// rather than a silent, unnoticed short read.
func (d *Driver) emitStream(scanner *bufio.Scanner, sink provider.StreamSink) error {
	for scanner.Scan() {
		chunk, err := decodeStreamChunk(scanner.Bytes())
		if err != nil {
			_ = sink(provider.StreamEvent{Kind: provider.StreamEventError, Err: err})
			return err
		}
		if chunk.errEvent != nil {
			_ = sink(provider.StreamEvent{Kind: provider.StreamEventError, Err: chunk.errEvent})
			return chunk.errEvent
		}
		if chunk.delta != "" {
			if serr := sink(provider.StreamEvent{Kind: provider.StreamEventDelta, Delta: chunk.delta}); serr != nil {
				return serr
			}
		}
		if chunk.done {
			return sink(provider.StreamEvent{Kind: provider.StreamEventDone, Usage: chunk.usage})
		}
	}
	if serr := scanner.Err(); serr != nil {
		wrapped := cascade.Wrap(cascade.KindUnavailable, serr, "ollama: reading stream body")
		_ = sink(provider.StreamEvent{Kind: provider.StreamEventError, Err: wrapped})
		return wrapped
	}
	truncated := cascade.New(cascade.KindIntegrity, "ollama: stream ended before a done chunk was received")
	_ = sink(provider.StreamEvent{Kind: provider.StreamEventError, Err: truncated})
	return truncated
}

// Stream implements provider.ModelProvider.Stream.
func (d *Driver) Stream(ctx context.Context, req provider.ChatRequest, sink provider.StreamSink) error {
	turns, err := buildMessages(req.Messages)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(wireChatRequest{
		Model:    d.cfg.model(req.Model, d.cfg.DefaultChatModel),
		Messages: turns,
		Stream:   true,
		Options:  chatOptions(req.MaxOutputTokens),
	})
	if err != nil {
		return cascade.Wrap(cascade.KindInvalidInput, err, "ollama: encoding stream request body")
	}
	httpReq, err := d.newRequest(ctx, "/api/chat", payload)
	if err != nil {
		return err
	}
	resp, err := d.cfg.Doer.Do(ctx, httpReq)
	if err != nil {
		return mapTransportError(ctx, err, d.cfg.baseURL())
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.Status < 200 || resp.Status >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxReadBody))
		return mapStatusError(resp.Status, body)
	}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), maxReadBody)
	return d.emitStream(scanner, sink)
}
