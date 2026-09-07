// Package openai implements the openai-compat pkg/provider.ModelProvider
// driver (04-PEWS-PLAN-W1-W3.md §Wave 3 §Epic J S-19.T3): ONE protocol
// implementation shared by the OpenAI Chat Completions API and every
// server that speaks the same shape (zai, Kimi/Moonshot, DeepSeek).
// 08-INIT-CONFIG-SPEC.md §2 fixes this: the [[providers]] kind enum has a
// single "openai-compat" member with a per-instance base_url - the kimi
// example (kind=openai-compat, base_url=https://api.moonshot.ai/v1) is a
// CONFIGURATION of this driver, never a protocol fork. R-14.103 struck the
// separate zai/ and kimi/ driver directories 02-TARGET-STRUCTURE.md had
// proposed for exactly this reason.
//
// Purpose: chat/embed/count/stream/capabilities over one openai-compat
//
//	server instance, key-auth only (no OAuth in this driver - P1's OAuth
//	scope per 06-FORGE-SPEC.md §5 rule 23 is anthropic + generic PKCE +
//	gemini official, and this compat family is not in that set).
//
// Inputs: a Config supplying the server's base_url, a vault key reference,
//
//	and every side-effecting dependency (HTTP transport, credential
//	resolver, clock) by injection.
//
// Outputs: pkg/provider's typed Chat/Embed/Count/Stream/Capabilities
//
//	results, or a pkg/cascade taxonomy error - never a raw error, never a
//	fabricated success.
//
// Constraints: providers/** may import pkg/** only, never internal/**
//
//	(02-TARGET-STRUCTURE.md §providers; enforced by .golangci.yml's
//	depguard AND internal/build's arch gate). That is why Clock and
//	KeyResolver are declared locally, structurally identical to
//	internal/runtime.Clock and the H/S-15 vault broker's Get, rather than
//	importing either: a *runtime.SystemClock or a broker-backed resolver
//	the composition root already holds satisfies these interfaces with no
//	adapter, because Go interface satisfaction is structural. Every
//	verified wire shape below (error envelopes, status codes) comes from a
//	live capture recorded in testdata/README.md; where this driver has not
//	verified a vendor's behaviour, its comment says so rather than
//	asserting it (Art.2).
//
// SPORT: providers/openai driver/ADD (P1-E10-W3-S19-T3).
package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// defaultRetryBaseDelay is Config.RetryBaseDelay's default when the caller
// leaves it zero: a tuning knob, not a required dependency, so New
// substitutes for it but never for BaseURL/KeyRef/Resolver/HTTPClient/Clock.
const defaultRetryBaseDelay = 500 * time.Millisecond

// maxRetryAttempts bounds doWithRetry's loop so a persistently failing
// vendor can never retry unboundedly.
const maxRetryAttempts = 3

// Doer performs one outbound HTTP call. Injected so the no-network unit
// lane (Art.7.2) can supply a recording fake instead of a real transport;
// production callers pass an *http.Client, which already satisfies this
// interface.
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Clock abstracts time.Now so retry-deadline decisions are provable with a
// frozen instant and no real sleeping (Art.7.3). Structurally identical to
// internal/runtime.Clock and internal/testkit.Clock; this package cannot
// import either (providers -> pkg only), so it declares its own - any
// concrete Clock those packages construct already satisfies this
// interface.
type Clock interface {
	Now() time.Time
}

// KeyResolver resolves a vault env-ref key name (08-INIT-CONFIG-SPEC.md
// §2's key_env: an environment VARIABLE NAME the intake reads once into
// the keychain, never a literal) to its current secret value, through the
// H/S-15 vault broker. This package never reads the environment or a
// vault directly - the caller wiring this driver (the S-20.T1 universal
// intake) supplies the concrete, broker-backed implementation.
type KeyResolver interface {
	// Resolve returns the current secret value stored under keyRef. A
	// resolver that cannot find keyRef returns a KindNotFound error; one
	// that cannot reach its backing store returns KindUnavailable.
	Resolve(ctx context.Context, keyRef string) (string, error)
}

// Config configures one openai-compat driver instance: one [[providers]]
// entry, one base_url, one key (08 §2).
type Config struct {
	// BaseURL is the compat server's API root, e.g.
	// "https://api.openai.com/v1" or "https://api.moonshot.ai/v1" (08 §2's
	// own kimi example). Required: https, or http restricted to the
	// loopback literal for a local self-hosted compat server.
	BaseURL string
	// KeyRef names the vault env-ref key. Required.
	KeyRef string
	// Resolver resolves KeyRef through the vault broker seam. Required.
	Resolver KeyResolver
	// HTTPClient performs outbound calls. Required.
	HTTPClient Doer
	// Clock is this driver's only time source. Required, with no
	// production default: a default would need a bare time.Now() call,
	// which forbidigo forbids outside internal/runtime/clock.go and
	// internal/testkit/clock.go, neither of which this package may import.
	Clock Clock
	// DefaultChatModel is used when a ChatRequest leaves Model empty.
	DefaultChatModel string
	// DefaultEmbedModel is used when a ModelEmbedRequest leaves Model empty.
	DefaultEmbedModel string
	// RetryBaseDelay is the backoff unit retryDelay's sequence scales from.
	// Zero is replaced with defaultRetryBaseDelay by New.
	RetryBaseDelay time.Duration
}

// Driver is the openai-compat provider.ModelProvider implementation. One
// Driver = one compat server instance = one [[providers]] entry (08 §2).
type Driver struct {
	cfg Config
}

// New validates cfg and returns a ready Driver. Every required dependency
// is checked explicitly; New substitutes a default only for the one
// tuning knob (RetryBaseDelay), never for a missing required dependency
// (Art.1: no silent stand-in that looks like it works).
func New(cfg Config) (*Driver, error) {
	if err := validateBaseURL(cfg.BaseURL); err != nil {
		return nil, err
	}
	if strings.TrimSpace(cfg.KeyRef) == "" {
		return nil, cascade.New(cascade.KindInvalidInput, "openai: config.KeyRef must not be empty")
	}
	if cfg.Resolver == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "openai: config.Resolver must not be nil")
	}
	if cfg.HTTPClient == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "openai: config.HTTPClient must not be nil")
	}
	if cfg.Clock == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "openai: config.Clock must not be nil")
	}
	if cfg.RetryBaseDelay <= 0 {
		cfg.RetryBaseDelay = defaultRetryBaseDelay
	}
	return &Driver{cfg: cfg}, nil
}

// validateBaseURL fails closed: any scheme other than https, or http on
// the loopback literal, is refused rather than silently accepted.
func validateBaseURL(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return cascade.New(cascade.KindInvalidInput, "openai: config.BaseURL must not be empty")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return cascade.Newf(cascade.KindInvalidInput, "openai: config.BaseURL %q is not a valid URL", raw)
	}
	if u.Scheme == "https" || (u.Scheme == "http" && u.Hostname() == "127.0.0.1") {
		return nil
	}
	return cascade.Newf(cascade.KindInvalidInput,
		"openai: config.BaseURL %q must be https, or http on 127.0.0.1 for a local compat server", raw)
}

// Chat implements provider.ModelProvider. The wire request/response types
// it marshals (wireMessage, chatCompletionRequest, chatCompletionResponse,
// embedding{Request,Datum,Response}) live in stream.go alongside the
// stream-chunk wire types they share a vocabulary with (300-line cap moved
// them there; toWireMessages/usageFromWire are the shared helpers).
func (d *Driver) Chat(ctx context.Context, req provider.ChatRequest) (provider.ChatResponse, error) {
	model := req.Model
	if model == "" {
		model = d.cfg.DefaultChatModel
	}
	wireReq := chatCompletionRequest{Model: model, Messages: toWireMessages(req.Messages), MaxTokens: req.MaxOutputTokens}
	body, err := json.Marshal(wireReq)
	if err != nil {
		return provider.ChatResponse{}, cascade.Wrap(cascade.KindInvalidInput, err, "openai: encoding chat request")
	}
	data, err := d.doWithRetry(ctx, http.MethodPost, "/chat/completions", body)
	if err != nil {
		return provider.ChatResponse{}, err
	}
	var wireResp chatCompletionResponse
	if jerr := json.Unmarshal(data, &wireResp); jerr != nil {
		return provider.ChatResponse{}, cascade.Wrap(cascade.KindIntegrity, jerr, "openai: decoding chat response")
	}
	if len(wireResp.Choices) == 0 {
		return provider.ChatResponse{}, cascade.New(cascade.KindIntegrity, "openai: chat response carried zero choices")
	}
	choice := wireResp.Choices[0]
	return provider.ChatResponse{
		Message:      provider.ChatMessage{Role: choice.Message.Role, Content: choice.Message.Content},
		Usage:        usageFromWire(wireResp.Usage),
		FinishReason: choice.FinishReason,
	}, nil
}

// Embed implements provider.ModelProvider.
func (d *Driver) Embed(ctx context.Context, req provider.ModelEmbedRequest) (provider.ModelEmbedResponse, error) {
	if len(req.Inputs) == 0 {
		return provider.ModelEmbedResponse{}, cascade.New(cascade.KindInvalidInput, "openai: embed request has no inputs")
	}
	model := req.Model
	if model == "" {
		model = d.cfg.DefaultEmbedModel
	}
	if model == "" {
		return provider.ModelEmbedResponse{}, cascade.New(cascade.KindInvalidInput, "openai: no embedding model configured")
	}
	body, err := json.Marshal(embeddingRequest{Model: model, Input: req.Inputs})
	if err != nil {
		return provider.ModelEmbedResponse{}, cascade.Wrap(cascade.KindInvalidInput, err, "openai: encoding embed request")
	}
	data, err := d.doWithRetry(ctx, http.MethodPost, "/embeddings", body)
	if err != nil {
		return provider.ModelEmbedResponse{}, err
	}
	var wireResp embeddingResponse
	if jerr := json.Unmarshal(data, &wireResp); jerr != nil {
		return provider.ModelEmbedResponse{}, cascade.Wrap(cascade.KindIntegrity, jerr, "openai: decoding embeddings response")
	}
	vectors := make([][]float32, len(req.Inputs))
	for _, datum := range wireResp.Data {
		if datum.Index >= 0 && datum.Index < len(vectors) {
			vectors[datum.Index] = datum.Embedding
		}
	}
	return provider.ModelEmbedResponse{Vectors: vectors, Usage: usageFromWire(wireResp.Usage)}, nil
}

// Count implements provider.ModelProvider. Verified against OpenAI's
// public API surface: this compat family exposes no server-side
// token-count endpoint. Some individual member servers add their own
// extension (e.g. Moonshot's /v1/tokenizers/estimate-token-count), but
// wiring a per-vendor extension would fork the protocol, which 08 §2's
// one-kind-per-base_url design and this ticket's scope both forbid - so
// Count is uniformly unsupported here rather than supported on some
// instances and not others. A caller needing an estimate uses
// pkg/provider's NaiveTokenCounter instead.
func (d *Driver) Count(_ context.Context, _ provider.CountRequest) (provider.CountResponse, error) {
	return provider.CountResponse{}, cascade.New(cascade.KindUnsupported,
		"openai: the openai-compat protocol exposes no server-side token-count endpoint")
}

// Capabilities implements provider.ModelProvider. This driver does not
// probe per-model tool-capability support (no live discovery call exists
// in this protocol today; R-21.28's future Discover() leg is not yet
// implemented anywhere), so every dimension is reported CapabilityUnknown
// rather than a guessed value (Art.1) - an honest "not yet probed", never
// a fabricated "supported". lane is accepted for interface conformance
// only: this driver's posture is uniform across every lane sharing one
// base_url/key, so lane selects nothing here.
func (d *Driver) Capabilities(_ context.Context, _ string) (provider.Capabilities, error) {
	posture := provider.NewCompliancePosture(
		[]string{"key"},
		false,
		true,
		nil,
		"vendor-defined; signaled via HTTP 429 (observed live on openai/moonshot/zai/deepseek "+
			"during fixture capture, see testdata/README.md); Retry-After is honored when a vendor sends it",
		false,
	)
	if err := posture.Validate(); err != nil {
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

// fakeResponse/fakeDoer/newFakeDoer (this package's no-network recording
// fake, Art.7.2) live in auth.go, which already imports everything they
// need for the transport layer they stand in for.
