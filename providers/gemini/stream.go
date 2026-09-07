// Purpose: transport plumbing and the streaming leg (Stream/emit) of the
//   gemini driver. decodeGeminiChunk is fuzzed by FuzzGeminiWireDecode.
// SPORT: placeholder: providers/gemini driver (ADD) - see gemini.go.

package gemini

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

// maxReadBody bounds buffered response size (memory-exhaustion guard).
const maxReadBody = 1 << 20 // 1 MiB

// doJSON issues one JSON exchange against path, retrying once on a 401.
func (d *Driver) doJSON(ctx context.Context, path string, body, out any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return cascade.Wrap(cascade.KindInvalidInput, err, "gemini: encoding request body")
	}
	resp, err := d.roundTrip(ctx, path, payload)
	if err != nil {
		return err
	}
	if resp.status < 200 || resp.status >= 300 {
		return mapStatusError(resp.status, resp.body)
	}
	if err := json.Unmarshal(resp.body, out); err != nil {
		return cascade.Wrap(cascade.KindIntegrity, err, "gemini: decoding response body")
	}
	return nil
}

// rawResponse is what roundTrip/send hand back.
type rawResponse struct {
	status int
	body   []byte
}

// Status codes named here, not imported from net/http, so this file needs
// no "net" import (Art.7.2).
const (
	statusBadRequest          = 400
	statusUnauthorized        = 401
	statusForbidden           = 403
	statusNotFound            = 404
	statusTooManyRequests     = 429
	statusInternalServerError = 500
)

// roundTrip retries once on a 401 after forcing an OAuth refresh (key-mode
// never retries: a bad key surfaces as a typed dead-key error instead).
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

// send performs one HTTP round trip, mapping failures onto the taxonomy.
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
		return rawResponse{}, cascade.Wrap(cascade.KindUnavailable, err, "gemini: reading response body")
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
			"content-type": "application/json",
			name:           value,
		},
		Body: payload,
	}, nil
}

// wireErrorDetail is the API's inner error object.
type wireErrorDetail struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Status  string `json:"status"`
}

// wireErrorBody is the API's error envelope, e.g.
// {"error":{"code":429,"message":"...","status":"RESOURCE_EXHAUSTED"}}.
type wireErrorBody struct {
	Error wireErrorDetail `json:"error"`
}

// isDeadKeyEnvelope reports a "dead"-key auth failure: Gemini reports an
// invalid key as HTTP 400, so message/status content distinguishes it.
func isDeadKeyEnvelope(wireErr wireErrorBody) bool {
	switch wireErr.Error.Status {
	case "UNAUTHENTICATED", "PERMISSION_DENIED":
		return true
	}
	msg := strings.ToLower(wireErr.Error.Message)
	return strings.Contains(msg, "api key not valid") || strings.Contains(msg, "api_key_invalid")
}

// mapStatusError classifies a status code (429/dead-key drive rotation).
func mapStatusError(status int, body []byte) error {
	var wireErr wireErrorBody
	_ = json.Unmarshal(body, &wireErr)
	msg := errorMessage(status, wireErr)
	switch {
	case status == statusTooManyRequests:
		return cascade.New(cascade.KindQuotaExhausted, msg)
	case status == statusUnauthorized, status == statusForbidden:
		return cascade.New(cascade.KindPermissionDenied, msg)
	case status == statusBadRequest && isDeadKeyEnvelope(wireErr):
		return cascade.New(cascade.KindPermissionDenied, msg)
	case status == statusBadRequest:
		return cascade.New(cascade.KindInvalidInput, msg)
	case status == statusNotFound:
		return cascade.New(cascade.KindNotFound, msg)
	case status >= statusInternalServerError:
		return cascade.New(cascade.KindUnavailable, msg)
	default:
		return cascade.New(cascade.KindInternal, msg)
	}
}

// errorMessage renders a diagnostic message, or a generic one when the
// body was not the documented error envelope.
func errorMessage(status int, wireErr wireErrorBody) string {
	if wireErr.Error.Message != "" {
		return fmt.Sprintf("gemini: http %d %s: %s", status, wireErr.Error.Status, wireErr.Error.Message)
	}
	return fmt.Sprintf("gemini: http %d", status)
}

// mapTransportError classifies a network failure: ctx cancel/deadline
// first, else KindUnavailable.
func mapTransportError(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.Canceled) {
		return cascade.Wrap(cascade.KindCanceled, err, "gemini: request canceled")
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return cascade.Wrap(cascade.KindTimeout, err, "gemini: request deadline exceeded")
	}
	return cascade.Wrap(cascade.KindUnavailable, err, "gemini: sending request")
}

// wireStreamChunk is one decoded SSE data payload: a partial
// GenerateContentResponse, or (rarely) a mid-stream error envelope.
type wireStreamChunk struct {
	Candidates    []wireCandidate    `json:"candidates,omitempty"`
	UsageMetadata *wireUsageMetadata `json:"usageMetadata,omitempty"`
	Error         *wireErrorDetail   `json:"error,omitempty"`
}

// decodeGeminiChunk turns one raw SSE "data:" payload into a
// provider.StreamEvent, or (zero value, false, nil) for a chunk with no
// text/usage/error to report. Malformed input is a KindIntegrity error,
// never a panic - FuzzGeminiWireDecode drives this function directly.
func decodeGeminiChunk(raw []byte) (provider.StreamEvent, bool, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return provider.StreamEvent{}, false, nil
	}
	var chunk wireStreamChunk
	if err := json.Unmarshal(trimmed, &chunk); err != nil {
		return provider.StreamEvent{}, false, cascade.Wrap(cascade.KindIntegrity, err, "gemini: decoding stream chunk")
	}
	if chunk.Error != nil {
		body, _ := json.Marshal(wireErrorBody{Error: *chunk.Error})
		return provider.StreamEvent{Kind: provider.StreamEventError, Err: mapStatusError(chunk.Error.Code, body)}, true, nil
	}
	if len(chunk.Candidates) > 0 {
		if text := candidateText(chunk.Candidates[0]); text != "" {
			return provider.StreamEvent{Kind: provider.StreamEventDelta, Delta: text}, true, nil
		}
	}
	if chunk.UsageMetadata != nil {
		usage := provider.Usage{InputTokens: chunk.UsageMetadata.PromptTokenCount, OutputTokens: chunk.UsageMetadata.CandidatesTokenCount}
		return provider.StreamEvent{Kind: provider.StreamEventUsage, Usage: usage}, true, nil
	}
	return provider.StreamEvent{}, false, nil
}

// scanGeminiSSE returns each complete blank-line-delimited "data:" payload,
// in order. Tolerant of a stream that ends mid-chunk (no trailing blank
// line): the accumulated partial chunk is still returned, so truncation
// surfaces to decodeGeminiChunk's error path instead of vanishing.
func scanGeminiSSE(r io.Reader) [][]byte {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), maxReadBody)
	var out [][]byte
	var data bytes.Buffer
	flush := func() {
		if data.Len() > 0 {
			// Clone: Bytes() aliases the buffer Reset() below reuses.
			out = append(out, bytes.Clone(bytes.TrimSuffix(data.Bytes(), []byte("\n"))))
		}
		data.Reset()
	}
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "":
			flush()
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
	system, turns, err := buildContents(req.Messages)
	if err != nil {
		return err
	}
	wireReq := wireGenerateRequest{Contents: turns, SystemInstruction: system}
	if req.MaxOutputTokens > 0 {
		wireReq.GenerationConfig = &wireGenerationConfig{MaxOutputTokens: req.MaxOutputTokens}
	}
	payload, err := json.Marshal(wireReq)
	if err != nil {
		return cascade.Wrap(cascade.KindInvalidInput, err, "gemini: encoding stream request body")
	}

	path := "/" + apiVersion + "/models/" + d.cfg.model(req.Model) + ":streamGenerateContent?alt=sse"
	httpReq, err := d.newRequest(ctx, path, payload)
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
	return d.emit(scanGeminiSSE(resp.Body), sink)
}

// emit delivers each decoded chunk to sink, guaranteeing exactly one
// terminal (done or error) delivery (R-21.217); Gemini's SSE has no
// explicit terminal marker, so a clean end synthesizes a done event.
func (d *Driver) emit(raw [][]byte, sink provider.StreamSink) error {
	for _, chunk := range raw {
		event, ok, err := decodeGeminiChunk(chunk)
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
		if event.Kind == provider.StreamEventError {
			return event.Err
		}
	}
	return sink(provider.StreamEvent{Kind: provider.StreamEventDone})
}
