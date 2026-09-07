// Package anthropic is the real Anthropic Messages-API driver
// (04-PEWS-PLAN-W1-W3.md §Wave 3 §Epic J S-19.T2): a concrete
// provider.ModelProvider (S-19.T1) over /v1/messages and
// /v1/messages/count_tokens, with both auth modes (auth.go) and the
// streaming leg (stream.go). No mock ships here: Embed (no such endpoint
// exists) returns a typed KindUnsupported error rather than a fabricated
// vector (Art.1). Inputs/outputs are a Config plus, per call, a
// context.Context and pkg/provider's request/response types, or a
// pkg/cascade taxonomy error. providers/** may import pkg/** only, never
// internal/**: Clock is a local structural interface internal/runtime.
// Clock also satisfies, and HTTPDoer uses this package's own
// HTTPRequest/HTTPResponse rather than net/http's, so the unit lane's
// no-"net"-import rule (Art.7.2) holds even for a fake with no real
// socket. No bare time.Now (Clock only).
//
// SPORT: placeholder: providers/anthropic driver (ADD: ModelProvider
// implementation, key+OAuth auth) - taxonomy per N/S-28.T1; master lists
// undefined until the engine exists (P1-E10-W3-S19-T2).
package anthropic

import (
	"context"
	"io"
	"strings"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// defaultBaseURL is the Messages API root.
const defaultBaseURL = "https://api.anthropic.com"

// anthropicVersion is the API version header every request carries.
const anthropicVersion = "2023-06-01"

// defaultModel is used when a request names none.
const defaultModel = "claude-3-5-sonnet-20241022"

// defaultMaxOutputTokens caps a reply when a request sets none (the API
// requires max_tokens on every request).
const defaultMaxOutputTokens = 4096

// HTTPRequest is one outbound request in this package's own vocabulary, not
// net/http.Request, so a recording fake implementing HTTPDoer never needs
// to import "net"/"net/http" (Art.7.2). A production caller wires a small
// *http.Client adapter (the intake/registry ticket's job, not this one's).
type HTTPRequest struct {
	Method  string
	URL     string
	Headers map[string]string
	Body    []byte
}

// HTTPResponse is one inbound response. Body streams so Stream can dispatch
// SSE events as they arrive rather than buffering first; callers Close it.
type HTTPResponse struct {
	Status int
	Body   io.ReadCloser
}

// HTTPDoer is the outbound HTTP seam every driver call goes through.
type HTTPDoer interface {
	Do(ctx context.Context, req HTTPRequest) (HTTPResponse, error)
}

// Clock abstracts the wall clock (02-TARGET-STRUCTURE §v1.1: no bare
// time.Now in domain logic). Declared locally, structurally identical to
// internal/runtime.Clock, because providers/** may not import internal/**.
type Clock interface {
	Now() time.Time
}

// Config configures one Driver instance.
type Config struct {
	// BaseURL overrides the API root; empty uses defaultBaseURL.
	BaseURL string
	// Doer is the outbound HTTP seam. Required.
	Doer HTTPDoer
	// Clock is the injected wall clock. Required.
	Clock Clock
	// Auth configures key or OAuth credential resolution. Required.
	Auth AuthConfig
	// DefaultModel overrides defaultModel when a request names none.
	DefaultModel string
}

func (c Config) validate() error {
	if c.Doer == nil {
		return cascade.New(cascade.KindInvalidInput, "anthropic: config.doer must not be nil")
	}
	if c.Clock == nil {
		return cascade.New(cascade.KindInvalidInput, "anthropic: config.clock must not be nil")
	}
	return c.Auth.validate()
}

func (c Config) baseURL() string {
	if strings.TrimSpace(c.BaseURL) != "" {
		return strings.TrimRight(c.BaseURL, "/")
	}
	return defaultBaseURL
}

func (c Config) model(requested string) string {
	if strings.TrimSpace(requested) != "" {
		return requested
	}
	if strings.TrimSpace(c.DefaultModel) != "" {
		return c.DefaultModel
	}
	return defaultModel
}

// Driver is the real provider.ModelProvider implementation over Anthropic's
// Messages API. The zero value is not usable; construct with New.
type Driver struct {
	cfg  Config
	auth authState
}

// New validates cfg and returns a ready Driver.
func New(cfg Config) (*Driver, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &Driver{cfg: cfg}, nil
}

// wireMessage is one turn in the Messages API's request body. Content is
// plain text (the real API also accepts a content-block array; every
// exchange here is text-only).
type wireMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// wireChatRequest is the /v1/messages request body.
type wireChatRequest struct {
	Model     string        `json:"model"`
	MaxTokens int           `json:"max_tokens"`
	System    string        `json:"system,omitempty"`
	Messages  []wireMessage `json:"messages"`
	Stream    bool          `json:"stream,omitempty"`
}

// wireContentBlock is one block of a response's content array.
type wireContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// wireUsage is the Messages API's usage object.
type wireUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// wireChatResponse is the non-streaming /v1/messages response body.
type wireChatResponse struct {
	ID         string             `json:"id"`
	Type       string             `json:"type"`
	Role       string             `json:"role"`
	Content    []wireContentBlock `json:"content"`
	Model      string             `json:"model"`
	StopReason string             `json:"stop_reason"`
	Usage      wireUsage          `json:"usage"`
}

// buildMessages splits req.Messages into the system string plus the
// user/assistant turn array; any other role is refused, never coerced.
func buildMessages(msgs []provider.ChatMessage) (system string, turns []wireMessage, err error) {
	var systemParts []string
	for _, m := range msgs {
		switch m.Role {
		case "system":
			systemParts = append(systemParts, m.Content)
		case "user", "assistant":
			turns = append(turns, wireMessage{Role: m.Role, Content: m.Content})
		default:
			return "", nil, cascade.Newf(cascade.KindInvalidInput,
				"anthropic: message role %q is not one of system/user/assistant", m.Role)
		}
	}
	if len(turns) == 0 {
		return "", nil, cascade.New(cascade.KindInvalidInput, "anthropic: chat request has no user/assistant turns")
	}
	return strings.Join(systemParts, "\n\n"), turns, nil
}

// finishReason normalizes Anthropic's stop_reason into the contract's
// example vocabulary where a direct mapping exists, and passes any other
// value through unchanged rather than inventing one.
func finishReason(stopReason string) string {
	switch stopReason {
	case "end_turn", "stop_sequence":
		return "stop"
	case "max_tokens":
		return "length"
	case "tool_use":
		return "tool_call"
	default:
		return stopReason
	}
}

// Chat implements provider.ModelProvider.Chat.
func (d *Driver) Chat(ctx context.Context, req provider.ChatRequest) (provider.ChatResponse, error) {
	system, turns, err := buildMessages(req.Messages)
	if err != nil {
		return provider.ChatResponse{}, err
	}
	maxTokens := req.MaxOutputTokens
	if maxTokens <= 0 {
		maxTokens = defaultMaxOutputTokens
	}
	wireReq := wireChatRequest{
		Model:     d.cfg.model(req.Model),
		MaxTokens: maxTokens,
		System:    system,
		Messages:  turns,
	}
	var resp wireChatResponse
	if err := d.doJSON(ctx, "/v1/messages", wireReq, &resp); err != nil {
		return provider.ChatResponse{}, err
	}
	var text strings.Builder
	for _, block := range resp.Content {
		if block.Type == "text" {
			text.WriteString(block.Text)
		}
	}
	return provider.ChatResponse{
		Message:      provider.ChatMessage{Role: "assistant", Content: text.String()},
		Usage:        provider.Usage{InputTokens: resp.Usage.InputTokens, OutputTokens: resp.Usage.OutputTokens},
		FinishReason: finishReason(resp.StopReason),
	}, nil
}

// Embed implements provider.ModelProvider.Embed: no such endpoint exists,
// so this returns a typed KindUnsupported error, never a fabricated vector.
func (d *Driver) Embed(_ context.Context, _ provider.ModelEmbedRequest) (provider.ModelEmbedResponse, error) {
	return provider.ModelEmbedResponse{}, cascade.New(cascade.KindUnsupported,
		"anthropic: the anthropic driver has no embeddings endpoint")
}

// wireCountRequest is the /v1/messages/count_tokens request body.
type wireCountRequest struct {
	Model    string        `json:"model"`
	System   string        `json:"system,omitempty"`
	Messages []wireMessage `json:"messages"`
}

// wireCountResponse is the /v1/messages/count_tokens response body.
type wireCountResponse struct {
	InputTokens int `json:"input_tokens"`
}

// Count implements provider.ModelProvider.Count against the real
// count_tokens endpoint.
func (d *Driver) Count(ctx context.Context, req provider.CountRequest) (provider.CountResponse, error) {
	wireReq := wireCountRequest{
		Model:    d.cfg.model(req.Model),
		Messages: []wireMessage{{Role: "user", Content: req.Text}},
	}
	var resp wireCountResponse
	if err := d.doJSON(ctx, "/v1/messages/count_tokens", wireReq, &resp); err != nil {
		return provider.CountResponse{}, err
	}
	return provider.CountResponse{Tokens: resp.InputTokens}, nil
}

// Capabilities implements provider.ModelProvider.Capabilities, advertising
// the real Anthropic capability descriptor plus the R-16.10 compliance
// posture. lane is accepted for interface conformance; this driver's
// posture does not currently vary per lane.
func (d *Driver) Capabilities(_ context.Context, _ string) (provider.Capabilities, error) {
	posture := provider.NewCompliancePosture(
		[]string{"api-key", "oauth"},
		true, // interactive_entitlement: oauth is a human-present login flow
		true, // programmatic_entitlement: api-key is a programmatic grant
		[]string{"batch", "agent", "scheduled"},
		"steady-with-burst",
		true, // multi_profile_enabled: a pool may hold more than one account
	)
	return provider.Capabilities{
		Search:            provider.CapabilityUnsupported,
		URLFetch:          provider.CapabilityUnsupported,
		Vision:            provider.CapabilitySupported,
		ToolUse:           provider.CapabilitySupported,
		LongContext:       provider.CapabilitySupported,
		StructuredOutput:  provider.CapabilitySupported,
		CompliancePosture: posture,
	}, nil
}

// doJSON, send, and the status/transport error mapping this method relies
// on live in stream.go alongside Stream, since Stream needs the identical
// request-construction and error-classification machinery.
