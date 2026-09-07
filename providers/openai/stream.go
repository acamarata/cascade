// Purpose: ModelProvider.Stream and this driver's own SSE decoder for the
//
//	openai-compat chat-completions stream: "data: <json>\n\n" events
//	terminated by "data: [DONE]\n\n". This decoder is the fuzzed surface
//	06-FORGE-SPEC.md §5 rule 7 requires (FuzzOpenAICompatWireDecode lives
//	in openai_test.go, since Go fuzz targets must be _test.go functions;
//	this file holds the decoder it exercises).
//
// Inputs: an io.Reader over one HTTP response body.
// Outputs: provider.StreamEvent values delivered to the caller's sink, in
//
//	order, terminating in exactly one done or error event (R-21.217).
//
// Constraints: never panics on malformed or truncated input - every
//
//	failure surfaces as a typed *cascade.Error through the terminal error
//	event and Stream's return value (Art.7).
//
// SPORT: providers/openai driver/ADD (P1-E10-W3-S19-T3).

package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// sseScanBufferCap bounds one buffered SSE line. A compat server that
// sends a single line larger than this is refused via scanner.Err()
// (bufio.ErrTooLong), surfaced as a KindIntegrity error - never silently
// truncated.
const sseScanBufferCap = 8 * 1024 * 1024

// wireMessage is one chat turn on the wire, shared by requests, responses
// and this file's stream-chunk delta shape. Declared here (not openai.go)
// only to keep openai.go under Art.10.3's 300-line cap - it and the
// non-streaming wire types below are Chat/Embed's (openai.go) request and
// response shapes, not part of this file's own SSE decoding.
type wireMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// streamOptions requests usage accounting on the final SSE chunk. OpenAI
// documents this field; it is sent unconditionally and simply ignored by
// a compat server that does not recognize it (verified: none of the four
// captures in testdata/README.md rejected an unrecognized field).
type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type chatCompletionRequest struct {
	Model         string         `json:"model"`
	Messages      []wireMessage  `json:"messages"`
	MaxTokens     int            `json:"max_tokens,omitempty"`
	Stream        bool           `json:"stream,omitempty"`
	StreamOptions *streamOptions `json:"stream_options,omitempty"`
}

type wireUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

type wireChoice struct {
	Message      wireMessage `json:"message"`
	FinishReason string      `json:"finish_reason"`
}

type chatCompletionResponse struct {
	Choices []wireChoice `json:"choices"`
	Usage   *wireUsage   `json:"usage"`
}

type embeddingRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embeddingDatum struct {
	Embedding []float32 `json:"embedding"`
	Index     int       `json:"index"`
}

type embeddingResponse struct {
	Data  []embeddingDatum `json:"data"`
	Usage *wireUsage       `json:"usage"`
}

// toWireMessages and usageFromWire are shared by openai.go's Chat/Embed and
// this file's emitChunk.
func toWireMessages(msgs []provider.ChatMessage) []wireMessage {
	out := make([]wireMessage, len(msgs))
	for i, m := range msgs {
		out[i] = wireMessage{Role: m.Role, Content: m.Content}
	}
	return out
}

func usageFromWire(u *wireUsage) provider.Usage {
	if u == nil {
		return provider.Usage{}
	}
	return provider.Usage{InputTokens: u.PromptTokens, OutputTokens: u.CompletionTokens}
}

// wireDelta is one stream chunk's incremental content.
type wireDelta struct {
	Content   string              `json:"content,omitempty"`
	ToolCalls []wireToolCallDelta `json:"tool_calls,omitempty"`
}

type wireFunctionDelta struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type wireToolCallDelta struct {
	Function wireFunctionDelta `json:"function"`
}

type wireChunkChoice struct {
	Delta wireDelta `json:"delta"`
}

type chatCompletionChunk struct {
	Choices []wireChunkChoice `json:"choices"`
	Usage   *wireUsage        `json:"usage"`
}

// parseSSEStream reads r as a sequence of SSE events, joining every
// "data:" line of one event with "\n" (the wire protocol allows a
// multi-line data field; every capture in testdata/README.md only ever
// sent one line per event, but this driver does not assume that), and
// calls onEvent with each event's joined data once a blank line (or EOF)
// closes it. Any other SSE field (event:, id:, retry:, or a ":" comment)
// is read and ignored - this driver has never observed a compat vendor
// send one, and the SSE spec itself tolerates unknown fields.
func parseSSEStream(r io.Reader, onEvent func(raw []byte) error) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), sseScanBufferCap)
	var dataLines []string
	flush := func() error {
		if len(dataLines) == 0 {
			return nil
		}
		raw := []byte(strings.Join(dataLines, "\n"))
		dataLines = dataLines[:0]
		return onEvent(raw)
	}
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "":
			if err := flush(); err != nil {
				return err
			}
		case strings.HasPrefix(line, "data:"):
			dataLines = append(dataLines, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		default:
			// event:/id:/retry:/comment - intentionally ignored.
		}
	}
	if err := scanner.Err(); err != nil {
		return cascade.Wrap(cascade.KindIntegrity, err, "openai: reading SSE stream")
	}
	return flush()
}

// decodeWireEvent decodes one SSE event's joined data as either the
// "[DONE]" sentinel or a chatCompletionChunk. A JSON decode failure is a
// KindIntegrity error (a schema-verification failure, per pkg/cascade's
// own definition of that Kind) - never a panic, never a silently-dropped
// event.
func decodeWireEvent(raw []byte) (chunk chatCompletionChunk, done bool, err error) {
	trimmed := bytes.TrimSpace(raw)
	if string(trimmed) == "[DONE]" {
		return chatCompletionChunk{}, true, nil
	}
	if uerr := json.Unmarshal(trimmed, &chunk); uerr != nil {
		return chatCompletionChunk{}, false, cascade.Wrap(cascade.KindIntegrity, uerr, "openai: decoding stream chunk")
	}
	return chunk, false, nil
}

// emitChunk delivers one decoded chunk's deltas, tool-call fragments and
// usage update to sink, in that order.
func emitChunk(chunk chatCompletionChunk, sink provider.StreamSink) error {
	for _, choice := range chunk.Choices {
		if choice.Delta.Content != "" {
			if err := sink(provider.StreamEvent{Kind: provider.StreamEventDelta, Delta: choice.Delta.Content}); err != nil {
				return err
			}
		}
		for _, tc := range choice.Delta.ToolCalls {
			ev := provider.StreamEvent{
				Kind:     provider.StreamEventToolCall,
				ToolCall: provider.ToolCall{Name: tc.Function.Name, Arguments: map[string]any{"raw_arguments": tc.Function.Arguments}},
			}
			if err := sink(ev); err != nil {
				return err
			}
		}
	}
	if chunk.Usage != nil {
		if err := sink(provider.StreamEvent{Kind: provider.StreamEventUsage, Usage: usageFromWire(chunk.Usage)}); err != nil {
			return err
		}
	}
	return nil
}

// mapStreamErr maps an error from parseSSEStream/decodeWireEvent (already
// typed) or a raw transport error (not yet typed) onto the taxonomy.
func mapStreamErr(err error) *cascade.Error {
	var taxErr *cascade.Error
	if errors.As(err, &taxErr) {
		return taxErr
	}
	return mapDoErr(err)
}

// Stream implements provider.ModelProvider.
func (d *Driver) Stream(ctx context.Context, req provider.ChatRequest, sink provider.StreamSink) error {
	model := req.Model
	if model == "" {
		model = d.cfg.DefaultChatModel
	}
	wireReq := chatCompletionRequest{
		Model: model, Messages: toWireMessages(req.Messages), MaxTokens: req.MaxOutputTokens,
		Stream: true, StreamOptions: &streamOptions{IncludeUsage: true},
	}
	body, err := json.Marshal(wireReq)
	if err != nil {
		return cascade.Wrap(cascade.KindInvalidInput, err, "openai: encoding stream request")
	}
	httpReq, err := d.newRequest(ctx, http.MethodPost, "/chat/completions", body)
	if err != nil {
		return err
	}
	resp, err := d.cfg.HTTPClient.Do(httpReq)
	if err != nil {
		terr := mapDoErr(err)
		_ = sink(provider.StreamEvent{Kind: provider.StreamEventError, Err: terr})
		return terr
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		terr := mapHTTPStatus(resp.StatusCode, data)
		_ = sink(provider.StreamEvent{Kind: provider.StreamEventError, Err: terr})
		return terr
	}
	return d.pumpStream(resp.Body, sink)
}

// pumpStream reads resp.Body's SSE events and delivers them to sink,
// guaranteeing exactly one terminal (done or error) event via the
// terminal bool latch (R-21.217's discipline; this contract does not
// enforce it for you).
func (d *Driver) pumpStream(body io.Reader, sink provider.StreamSink) error {
	var terminal bool
	emitTerminal := func(ev provider.StreamEvent) error {
		if terminal {
			return nil
		}
		terminal = true
		return sink(ev)
	}
	perr := parseSSEStream(body, func(raw []byte) error {
		chunk, done, derr := decodeWireEvent(raw)
		if derr != nil {
			return derr
		}
		if done {
			return emitTerminal(provider.StreamEvent{Kind: provider.StreamEventDone})
		}
		return emitChunk(chunk, sink)
	})
	if perr != nil {
		terr := mapStreamErr(perr)
		_ = emitTerminal(provider.StreamEvent{Kind: provider.StreamEventError, Err: terr})
		return terr
	}
	if !terminal {
		terr := cascade.New(cascade.KindUnavailable, "openai: stream ended without a terminal event (truncated)")
		_ = emitTerminal(provider.StreamEvent{Kind: provider.StreamEventError, Err: terr})
		return terr
	}
	return nil
}
