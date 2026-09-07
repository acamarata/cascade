// Package gemini is the real Gemini Generative Language API driver
// (04-PEWS-PLAN-W1-W3.md §Wave 3 §Epic J S-19.T4): a concrete
// provider.ModelProvider (S-19.T1) over generateContent, embedContent (via
// batchEmbedContents, auth.go), countTokens (stream.go), and
// streamGenerateContent (stream.go), with key and official-OAuth auth
// (auth.go). PROVIDER ONLY - no gemini harness exists in the product.
// Key-POOL lanes (GF pattern): pool-stateless - one verb, one caller-
// resolved vault-ref key, 429/dead-key failures surfaced as typed errors;
// no pool index, no rotation (S-20.T2/T3 own that). The consumer-sub
// cloudcode path is a private config-side extension (§D-9/Q-4), never
// named or special-cased here. providers/** import pkg/** only, never
// internal/**: Clock is a local structural interface internal/runtime.Clock
// also satisfies; HTTPDoer uses this package's own request/response types,
// not net/http's, so the unit lane's no-"net"-import rule (Art.7.2) holds.
//
// SPORT: placeholder: providers/gemini driver (ADD: ModelProvider
// implementation, key+official-OAuth auth, key-pool lanes) - taxonomy per
// N/S-28.T1; master lists undefined until the engine exists
// (P1-E10-W3-S19-T4).
package gemini

import (
	"context"
	"io"
	"strings"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

const (
	defaultBaseURL    = "https://generativelanguage.googleapis.com" // API root
	apiVersion        = "v1beta"                                    // API surface every request targets
	defaultModel      = "gemini-1.5-flash"                          // used when a request names none
	defaultEmbedModel = "text-embedding-004"                        // used when an embed request names none
)

// HTTPRequest is one outbound request in this package's own vocabulary
// (not net/http.Request), so a fake HTTPDoer never needs "net" (Art.7.2).
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

// Clock abstracts the wall clock (no bare time.Now in domain logic);
// structurally identical to internal/runtime.Clock, declared locally
// since providers/** may not import internal/**.
type Clock interface {
	Now() time.Time
}

// Config configures one Driver instance. Doer, Clock and Auth are required;
// BaseURL/DefaultModel/DefaultEmbedModel fall back to package defaults.
type Config struct {
	BaseURL           string
	Doer              HTTPDoer
	Clock             Clock
	Auth              AuthConfig
	DefaultModel      string
	DefaultEmbedModel string
}

func (c Config) validate() error {
	if c.Doer == nil {
		return cascade.New(cascade.KindInvalidInput, "gemini: config.doer must not be nil")
	}
	if c.Clock == nil {
		return cascade.New(cascade.KindInvalidInput, "gemini: config.clock must not be nil")
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

func (c Config) embedModel(requested string) string {
	if strings.TrimSpace(requested) != "" {
		return requested
	}
	if strings.TrimSpace(c.DefaultEmbedModel) != "" {
		return c.DefaultEmbedModel
	}
	return defaultEmbedModel
}

// Driver is the real provider.ModelProvider implementation over the
// Gemini API. The zero value is not usable; construct with New.
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

// wirePart is one part of a content block (text-only here).
type wirePart struct {
	Text string `json:"text"`
}

// wireContent is one turn: a role ("user"/"model") and its parts.
type wireContent struct {
	Role  string     `json:"role,omitempty"`
	Parts []wirePart `json:"parts"`
}

// wireGenerationConfig carries the generation knobs this driver sets.
type wireGenerationConfig struct {
	MaxOutputTokens int `json:"maxOutputTokens,omitempty"`
}

// wireGenerateRequest is the generateContent/streamGenerateContent body.
type wireGenerateRequest struct {
	Contents          []wireContent         `json:"contents"`
	SystemInstruction *wireContent          `json:"systemInstruction,omitempty"`
	GenerationConfig  *wireGenerationConfig `json:"generationConfig,omitempty"`
}

// wireUsageMetadata is the API's usage object.
type wireUsageMetadata struct {
	PromptTokenCount     int `json:"promptTokenCount"`
	CandidatesTokenCount int `json:"candidatesTokenCount"`
	TotalTokenCount      int `json:"totalTokenCount"`
}

// wireCandidate is one candidate reply in a generateContent response.
type wireCandidate struct {
	Content      wireContent `json:"content"`
	FinishReason string      `json:"finishReason"`
	Index        int         `json:"index"`
}

// wireGenerateResponse is the non-streaming generateContent response body.
type wireGenerateResponse struct {
	Candidates    []wireCandidate    `json:"candidates"`
	UsageMetadata *wireUsageMetadata `json:"usageMetadata,omitempty"`
}

// buildContents splits req.Messages into an optional systemInstruction plus
// the user/model turn array; any role other than system/user/assistant is
// refused, never coerced (Gemini has no direct "tool" content role this
// minimal chat/stream contract translates to).
func buildContents(msgs []provider.ChatMessage) (system *wireContent, turns []wireContent, err error) {
	var systemParts []string
	for _, m := range msgs {
		switch m.Role {
		case "system":
			systemParts = append(systemParts, m.Content)
		case "user":
			turns = append(turns, wireContent{Role: "user", Parts: []wirePart{{Text: m.Content}}})
		case "assistant":
			turns = append(turns, wireContent{Role: "model", Parts: []wirePart{{Text: m.Content}}})
		default:
			return nil, nil, cascade.Newf(cascade.KindInvalidInput,
				"gemini: message role %q is not one of system/user/assistant", m.Role)
		}
	}
	if len(turns) == 0 {
		return nil, nil, cascade.New(cascade.KindInvalidInput, "gemini: chat request has no user/assistant turns")
	}
	if len(systemParts) > 0 {
		system = &wireContent{Parts: []wirePart{{Text: strings.Join(systemParts, "\n\n")}}}
	}
	return system, turns, nil
}

// finishReason maps Gemini's finishReason onto the contract's vocabulary
// where a direct mapping exists, passing anything else through lowercased.
func finishReason(reason string) string {
	switch reason {
	case "STOP":
		return "stop"
	case "MAX_TOKENS":
		return "length"
	default:
		return strings.ToLower(reason)
	}
}

// candidateText concatenates every text part of one candidate's content.
func candidateText(c wireCandidate) string {
	var b strings.Builder
	for _, p := range c.Content.Parts {
		b.WriteString(p.Text)
	}
	return b.String()
}

// Chat implements provider.ModelProvider.Chat.
func (d *Driver) Chat(ctx context.Context, req provider.ChatRequest) (provider.ChatResponse, error) {
	system, turns, err := buildContents(req.Messages)
	if err != nil {
		return provider.ChatResponse{}, err
	}
	wireReq := wireGenerateRequest{Contents: turns, SystemInstruction: system}
	if req.MaxOutputTokens > 0 {
		wireReq.GenerationConfig = &wireGenerationConfig{MaxOutputTokens: req.MaxOutputTokens}
	}
	var resp wireGenerateResponse
	path := "/" + apiVersion + "/models/" + d.cfg.model(req.Model) + ":generateContent"
	if err := d.doJSON(ctx, path, wireReq, &resp); err != nil {
		return provider.ChatResponse{}, err
	}
	if len(resp.Candidates) == 0 {
		return provider.ChatResponse{}, cascade.New(cascade.KindIntegrity, "gemini: response carried no candidates")
	}
	cand := resp.Candidates[0]
	usage := provider.Usage{}
	if resp.UsageMetadata != nil {
		usage = provider.Usage{InputTokens: resp.UsageMetadata.PromptTokenCount, OutputTokens: resp.UsageMetadata.CandidatesTokenCount}
	}
	return provider.ChatResponse{
		Message:      provider.ChatMessage{Role: "assistant", Content: candidateText(cand)},
		Usage:        usage,
		FinishReason: finishReason(cand.FinishReason),
	}, nil
}

// wireCountRequest is the countTokens request body.
type wireCountRequest struct {
	Contents []wireContent `json:"contents"`
}

// wireCountResponse is the countTokens response body.
type wireCountResponse struct {
	TotalTokens int `json:"totalTokens"`
}

// Count implements provider.ModelProvider.Count against the real
// countTokens endpoint.
func (d *Driver) Count(ctx context.Context, req provider.CountRequest) (provider.CountResponse, error) {
	wireReq := wireCountRequest{Contents: []wireContent{{Role: "user", Parts: []wirePart{{Text: req.Text}}}}}
	var resp wireCountResponse
	path := "/" + apiVersion + "/models/" + d.cfg.model(req.Model) + ":countTokens"
	if err := d.doJSON(ctx, path, wireReq, &resp); err != nil {
		return provider.CountResponse{}, err
	}
	return provider.CountResponse{Tokens: resp.TotalTokens}, nil
}

// Capabilities implements provider.ModelProvider.Capabilities: the real
// Gemini capability descriptor plus the R-16.10 compliance posture. lane
// does not currently vary this driver's posture (pool-member tiers are
// the registry's concern, S-20.T2/T3, not this stateless driver's).
func (d *Driver) Capabilities(_ context.Context, _ string) (provider.Capabilities, error) {
	posture := provider.NewCompliancePosture(
		[]string{"api-key", "oauth"},
		true, // interactive_entitlement: official oauth is a human-present login flow
		true, // programmatic_entitlement: api-key is a programmatic grant
		[]string{"batch", "agent", "scheduled"},
		"burst-then-cooldown", // free-tier key-pool lanes see bursty per-key rate limits
		true,                  // multi_profile_enabled: a key pool holds more than one account/key
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

// Embed lives in auth.go (file-size balance under Art.10.3's 300-line
// cap); doJSON and the transport/error-mapping machinery live in stream.go
// alongside Stream, which needs the identical plumbing.
