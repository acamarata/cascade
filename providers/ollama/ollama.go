// Package ollama is the real Ollama HTTP-API driver (04-PEWS-PLAN-W1-W3.md
// §Wave 3 §Epic J S-19.T5): a concrete provider.ModelProvider (S-19.T1)
// over a local (or remote) Ollama server's /api/chat, /api/embed, and
// /api/tags, with the streaming leg in stream.go. Ollama is a LOCAL
// server: nothing listening on the configured port maps to a clear
// KindUnavailable naming the base URL, not a bare dial error
// (mapTransportError, stream.go). Count returns typed KindUnsupported
// (no server-side token-count endpoint), never a fabricated value
// (Art.1). providers/** may import pkg/** only, hence the local
// Clock/KeyResolver and own HTTPRequest/HTTPResponse (Art.7.2). No bare
// time.Now.
//
// SPORT: placeholder: providers/ollama driver (ADD) (P1-E10-W3-S19-T5).
package ollama

import (
	"context"
	"io"
	"strings"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// defaultBaseURL is a default local Ollama server's API root (08-INIT-
// CONFIG-SPEC.md §2's [[providers]] base_url overrides this per-instance).
const defaultBaseURL = "http://localhost:11434"

// HTTPRequest is one outbound request in this package's own vocabulary, not
// net/http.Request (Art.7.2). A production caller wires a *http.Client
// adapter.
type HTTPRequest struct {
	Method  string
	URL     string
	Headers map[string]string
	Body    []byte
}

// HTTPResponse is one inbound response; callers Close Body.
type HTTPResponse struct {
	Status int
	Body   io.ReadCloser
}

// HTTPDoer is the outbound HTTP seam every driver call goes through.
type HTTPDoer interface {
	Do(ctx context.Context, req HTTPRequest) (HTTPResponse, error)
}

// Clock abstracts the wall clock (no bare time.Now). Declared locally,
// structurally identical to internal/runtime.Clock (providers/** may not
// import internal/**).
type Clock interface {
	Now() time.Time
}

// KeyResolver dereferences a vault-key NAME to its secret value through
// the H/S-15 vault broker; this package never reads env/vault directly.
type KeyResolver interface {
	Resolve(ctx context.Context, ref string) (string, error)
}

// Config configures one Driver instance: one [[providers]] entry (08 §2).
type Config struct {
	// BaseURL overrides the API root; empty uses defaultBaseURL.
	BaseURL string
	// Doer is the outbound HTTP seam. Required.
	Doer HTTPDoer
	// Clock is the injected wall clock. Required.
	Clock Clock
	// TokenRef optionally names the vault key holding a Bearer token for a
	// remote/auth-gated instance; empty means none required (the common
	// local-deployment case).
	TokenRef string
	// Resolver dereferences TokenRef. Required only when TokenRef is set.
	Resolver KeyResolver
	// DefaultChatModel is used when a ChatRequest leaves Model empty.
	DefaultChatModel string
	// DefaultEmbedModel is used when a ModelEmbedRequest leaves Model empty.
	DefaultEmbedModel string
}

func (c Config) validate() error {
	if c.Doer == nil {
		return cascade.New(cascade.KindInvalidInput, "ollama: config.doer must not be nil")
	}
	if c.Clock == nil {
		return cascade.New(cascade.KindInvalidInput, "ollama: config.clock must not be nil")
	}
	if strings.TrimSpace(c.TokenRef) != "" && c.Resolver == nil {
		return cascade.New(cascade.KindInvalidInput,
			"ollama: config.resolver must not be nil when config.tokenref names a token")
	}
	return nil
}

func (c Config) baseURL() string {
	if strings.TrimSpace(c.BaseURL) != "" {
		return strings.TrimRight(c.BaseURL, "/")
	}
	return defaultBaseURL
}

func (c Config) model(requested, fallback string) string {
	if strings.TrimSpace(requested) != "" {
		return requested
	}
	return fallback
}

// Driver is the real provider.ModelProvider over the Ollama HTTP API. The
// zero value is not usable; construct with New.
type Driver struct{ cfg Config }

// New validates cfg and returns a ready Driver.
func New(cfg Config) (*Driver, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &Driver{cfg: cfg}, nil
}

// wireMessage is one chat turn - system inline in the array, unlike
// Anthropic's separate top-level field.
type wireMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// wireOptions carries generation-tuning fields.
type wireOptions struct {
	NumPredict int `json:"num_predict,omitempty"`
}

// wireChatRequest is the /api/chat request body.
type wireChatRequest struct {
	Model    string        `json:"model"`
	Messages []wireMessage `json:"messages"`
	Stream   bool          `json:"stream"`
	Options  *wireOptions  `json:"options,omitempty"`
}

// wireChatResponse is the non-streaming /api/chat response.
type wireChatResponse struct {
	Model           string      `json:"model"`
	Message         wireMessage `json:"message"`
	Done            bool        `json:"done"`
	DoneReason      string      `json:"done_reason"`
	PromptEvalCount int         `json:"prompt_eval_count"`
	EvalCount       int         `json:"eval_count"`
}

// buildMessages validates msgs and passes them through to the wire shape; any role other than system/user/assistant/tool is refused.
func buildMessages(msgs []provider.ChatMessage) ([]wireMessage, error) {
	if len(msgs) == 0 {
		return nil, cascade.New(cascade.KindInvalidInput, "ollama: chat request has no messages")
	}
	turns := make([]wireMessage, 0, len(msgs))
	for _, m := range msgs {
		switch m.Role {
		case "system", "user", "assistant", "tool":
			turns = append(turns, wireMessage{Role: m.Role, Content: m.Content})
		default:
			return nil, cascade.Newf(cascade.KindInvalidInput,
				"ollama: message role %q is not one of system/user/assistant/tool", m.Role)
		}
	}
	return turns, nil
}

// chatOptions builds *wireOptions for maxOutputTokens, or nil when the caller set no cap (Ollama's own default applies).
func chatOptions(maxOutputTokens int) *wireOptions {
	if maxOutputTokens <= 0 {
		return nil
	}
	return &wireOptions{NumPredict: maxOutputTokens}
}

// Chat implements provider.ModelProvider.Chat.
func (d *Driver) Chat(ctx context.Context, req provider.ChatRequest) (provider.ChatResponse, error) {
	turns, err := buildMessages(req.Messages)
	if err != nil {
		return provider.ChatResponse{}, err
	}
	wireReq := wireChatRequest{
		Model:    d.cfg.model(req.Model, d.cfg.DefaultChatModel),
		Messages: turns,
		Stream:   false,
		Options:  chatOptions(req.MaxOutputTokens),
	}
	var resp wireChatResponse
	if err := d.doJSON(ctx, "/api/chat", wireReq, &resp); err != nil {
		return provider.ChatResponse{}, err
	}
	return provider.ChatResponse{
		Message:      provider.ChatMessage{Role: resp.Message.Role, Content: resp.Message.Content},
		Usage:        provider.Usage{InputTokens: resp.PromptEvalCount, OutputTokens: resp.EvalCount},
		FinishReason: resp.DoneReason,
	}, nil
}

// Count implements provider.ModelProvider.Count: no server-side token-count endpoint exists, so this returns typed KindUnsupported, never a fabricated count (Art.1); NaiveTokenCounter is the estimate fallback.
func (d *Driver) Count(_ context.Context, _ provider.CountRequest) (provider.CountResponse, error) {
	return provider.CountResponse{}, cascade.New(cascade.KindUnsupported,
		"ollama: the ollama http api exposes no server-side token-count endpoint")
}

// wireEmbedRequest is the /api/embed request body: a batch of inputs, unlike Anthropic (no embed endpoint) or openai-compat's single input.
type wireEmbedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

// wireEmbedResponse is the /api/embed response.
type wireEmbedResponse struct {
	Embeddings      [][]float32 `json:"embeddings"`
	PromptEvalCount int         `json:"prompt_eval_count"`
}

// Embed implements provider.ModelProvider.Embed: genuine embedding support, implemented rather than returning KindUnsupported.
func (d *Driver) Embed(ctx context.Context, req provider.ModelEmbedRequest) (provider.ModelEmbedResponse, error) {
	if len(req.Inputs) == 0 {
		return provider.ModelEmbedResponse{}, cascade.New(cascade.KindInvalidInput, "ollama: embed request has no inputs")
	}
	model := d.cfg.model(req.Model, d.cfg.DefaultEmbedModel)
	if model == "" {
		return provider.ModelEmbedResponse{}, cascade.New(cascade.KindInvalidInput, "ollama: no embedding model configured")
	}
	var resp wireEmbedResponse
	if err := d.doJSON(ctx, "/api/embed", wireEmbedRequest{Model: model, Input: req.Inputs}, &resp); err != nil {
		return provider.ModelEmbedResponse{}, err
	}
	if len(resp.Embeddings) != len(req.Inputs) {
		return provider.ModelEmbedResponse{}, cascade.Newf(cascade.KindIntegrity,
			"ollama: embed response carried %d vectors for %d inputs", len(resp.Embeddings), len(req.Inputs))
	}
	return provider.ModelEmbedResponse{
		Vectors: resp.Embeddings,
		Usage:   provider.Usage{InputTokens: resp.PromptEvalCount},
	}, nil
}

// wireTagsResponse is the /api/tags response body: the models currently installed on this Ollama server.
type wireTagsResponse struct {
	Models []struct {
		Name string `json:"name"`
	} `json:"models"`
}

// tagsContain reports whether tags lists model by name.
func tagsContain(tags wireTagsResponse, model string) bool {
	for _, m := range tags.Models {
		if m.Name == model {
			return true
		}
	}
	return false
}

// compliancePosture builds this driver's R-16.10 posture: interactive_entitlement is false (a local server needs no human-present login flow); programmatic_entitlement tracks whether a bearer token is configured.
func (d *Driver) compliancePosture() (provider.CompliancePosture, error) {
	authModes := []string{"none"}
	if strings.TrimSpace(d.cfg.TokenRef) != "" {
		authModes = []string{"none", "optional-bearer"}
	}
	posture := provider.NewCompliancePosture(
		authModes, false, strings.TrimSpace(d.cfg.TokenRef) != "",
		[]string{"batch", "agent", "scheduled"},
		"no vendor rate limiting; bounded only by local hardware throughput", false,
	)
	return posture, posture.Validate()
}

// Capabilities implements provider.ModelProvider.Capabilities against the real /api/tags endpoint - proving reachability and, when lane names a model, that it is installed. No live discovery call exists in the Ollama API, so every R-14.88 dimension reports CapabilityUnknown rather than a guessed value (Art.1).
func (d *Driver) Capabilities(ctx context.Context, lane string) (provider.Capabilities, error) {
	var tags wireTagsResponse
	if err := d.doGET(ctx, "/api/tags", &tags); err != nil {
		return provider.Capabilities{}, err
	}
	if lane != "" && !tagsContain(tags, lane) {
		return provider.Capabilities{}, cascade.Newf(cascade.KindNotFound,
			"ollama: model %q is not among the locally installed models", lane)
	}
	posture, err := d.compliancePosture()
	if err != nil {
		return provider.Capabilities{}, err
	}
	return provider.Capabilities{
		Search:            provider.CapabilityUnknown,
		URLFetch:          provider.CapabilityUnknown,
		Vision:            provider.CapabilityUnknown,
		ToolUse:           provider.CapabilityUnknown,
		LongContext:       provider.CapabilityUnknown,
		StructuredOutput:  provider.CapabilityUnknown,
		CompliancePosture: posture,
	}, nil
}

// doJSON, doGET, and HTTP/error-mapping live in stream.go, alongside Stream.
