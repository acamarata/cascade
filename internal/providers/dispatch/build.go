// Purpose: the DriverKind -> providers/* driver switch Resolver.Resolve
//
//	calls once a provider record's credential is known to be resolvable
//	(Resolve itself already refused an oauth-mode record or a missing
//	CredentialSource before reaching here - see resolver.go). Each case
//	is a real providers/* constructor call, never a fabricated stand-in.
//
// Inputs: a real registry.ProviderRecord (BaseURL, AuthRef, DriverKind)
//
//	and the Selection's resolved model name.
//
// Outputs: a live provider.ModelProvider (the real driver inside the
//
//	lane-outcome decorator, lane_outcome.go), or KindUnsupported for a
//
//	DriverKind this composition root has no driver for yet
//	(registry.DriverLocalLLM: no providers/localllm package exists in
//	this tree).
//
// Constraints: r.credentials is guaranteed non-nil by Resolve's own guard
//
//	before this method runs; it is passed straight through as each
//	driver's own KeyResolver, never dereferenced here, so the actual
//	secret value is fetched by the driver itself, at request time, per
//	every providers/* driver's own documented contract ("dereferenced
//	only through Resolver at request time").
//
// SPORT: internal/providers/dispatch (ADD, DEFECT-conductor-execute-
//
//	permanently-unavailable.md).

package dispatch

import (
	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/providers/anthropic"
	"github.com/acamarata/cascade/providers/gemini"
	"github.com/acamarata/cascade/providers/ollama"
	"github.com/acamarata/cascade/providers/openai"
	"github.com/acamarata/cascade/providers/transport"
)

// build constructs the live provider.ModelProvider named by rec, with
// model as the driver's default model (the only channel a resolved
// Selection.Model reaches the driver through today - execute.go's own
// toChatRequest never sets ChatRequest.Model, a separate, already
// disclosed gap this ticket's files_scope does not include).
//
// The driver sends through an observing Transport and is returned inside
// the lane-outcome decorator (lane_outcome.go), so every call it makes
// records its outcome on the lane. The decorator's Unwrap returns the
// real driver.
func (r *Resolver) build(lane registry.LaneRecord, rec registry.ProviderRecord, model string) (provider.ModelProvider, error) {
	driver, err := r.newDriver(rec, model, observingTransport{inner: r.transport})
	if err != nil {
		return nil, err
	}
	recorder := &laneRecorder{lookup: r.lookup, lane: lane.LaneName, clock: r.clock, log: r.log, mu: &r.laneMu}
	return &laneOutcomeProvider{inner: driver, rec: recorder}, nil
}

// newDriver is the DriverKind switch: one real providers/* constructor per
// kind, every one sending through tr.
func (r *Resolver) newDriver(rec registry.ProviderRecord, model string, tr transport.Transport) (provider.ModelProvider, error) {
	switch rec.Driver {
	case registry.DriverAnthropic:
		return anthropic.New(anthropic.Config{
			BaseURL: rec.BaseURL,
			Doer:    transport.AnthropicDoer{Transport: tr},
			Clock:   r.clock,
			Auth: anthropic.AuthConfig{
				Mode:     anthropic.AuthModeKey,
				KeyRef:   rec.AuthRef.String(),
				Resolver: r.credentials,
			},
			DefaultModel: model,
		})
	case registry.DriverGemini:
		return gemini.New(gemini.Config{
			BaseURL: rec.BaseURL,
			Doer:    transport.GeminiDoer{Transport: tr},
			Clock:   r.clock,
			Auth: gemini.AuthConfig{
				Mode:     gemini.AuthModeKey,
				KeyRef:   rec.AuthRef.String(),
				Resolver: r.credentials,
			},
			DefaultModel: model,
		})
	case registry.DriverOllama:
		return ollama.New(ollama.Config{
			BaseURL:          rec.BaseURL,
			Doer:             transport.OllamaDoer{Transport: tr},
			Clock:            r.clock,
			TokenRef:         rec.AuthRef.String(),
			Resolver:         r.credentials,
			DefaultChatModel: model,
		})
	case registry.DriverOpenAICompat:
		return openai.New(openai.Config{
			BaseURL:          rec.BaseURL,
			KeyRef:           rec.AuthRef.String(),
			Resolver:         r.credentials,
			HTTPClient:       transport.OpenAIDoer{Transport: tr},
			Clock:            r.clock,
			DefaultChatModel: model,
		})
	case registry.DriverLocalLLM:
		// No providers/localllm package exists in this tree yet (see this
		// file's header comment); named explicitly rather than falling
		// through default so the exhaustive linter proves every DriverKind
		// member was considered, not merely caught by a catch-all.
		return nil, cascade.Newf(cascade.KindUnsupported,
			"dispatch: driver kind %q has no production dispatch implementation in this tree yet", rec.Driver)
	default:
		return nil, cascade.Newf(cascade.KindUnsupported,
			"dispatch: driver kind %q has no production dispatch implementation in this tree yet", rec.Driver)
	}
}
