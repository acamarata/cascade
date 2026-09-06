// Purpose: the ModelProvider contract (04-PEWS-PLAN-W1-W3.md §Wave 3 §Epic
//   J S-19.T1): the driver-facing interface every J/S-19 provider (T2
//   anthropic, T3 openai-compat, T4 gemini, T5 ollama) implements, with its
//   typed request/response models and the capabilities descriptor K/S-22.T2
//   matches routing requirements against.
// Inputs: none at this layer - these are contracts, not behavior.
// Outputs: none.
// Constraints: exactly five verbs - chat, embed, count, stream, capabilities
//   - and no sixth (06 §5 rule 11). Amended by R-21.28: AN/S-77.T5 (W9) adds
//   a sixth method Discover(ctx) ([]LaneOffering, error) as a
//   files_scope.change; this interface is not closed against that ruling,
//   it simply does not implement it yet. Vendor-neutral and auth-free: no
//   provider-specific type or credential shape crosses this boundary -
//   credentials resolve through the H/S-15 broker (06 §5 rule 23), which
//   drivers reach through ProviderOAuthConfig (oauth_types.go), not through
//   any field here. ctx is always the crossing function's first parameter
//   and is never stored in a struct.
// SPORT: pkg.provider.modelprovider-contract/ADD (P1-E10-W3-S19-T1).

package provider

import "context"

// ChatMessage is one turn of a chat exchange: a role and its text content.
// It is intentionally minimal and vendor-neutral - a driver translates it to
// and from whatever wire shape its own vendor API expects.
type ChatMessage struct {
	// Role names the turn's speaker: "system", "user", "assistant", or
	// "tool". Kept as a plain string rather than a closed enum since
	// drivers are free to pass through additional vendor-defined roles
	// without this contract needing an amendment for each one.
	Role string
	// Content is the turn's text.
	Content string
}

// ChatRequest is the input to ModelProvider.Chat and the streaming leg of
// ModelProvider.Stream.
type ChatRequest struct {
	// Messages is the ordered conversation to complete.
	Messages []ChatMessage
	// Model optionally names the specific model within the provider to use;
	// empty defers to the driver's own default.
	Model string
	// MaxOutputTokens caps the reply length; zero means "use the driver's
	// own default cap".
	MaxOutputTokens int
	// RequiredCapabilities lets a caller assert, at the driver boundary,
	// which R-14.88 tool-capability dimensions this exchange needs; a
	// driver that cannot satisfy them returns a taxonomy error rather than
	// silently degrading.
	RequiredCapabilities RequiredCapabilities
}

// ChatResponse is the result of ModelProvider.Chat.
type ChatResponse struct {
	// Message is the model's reply turn.
	Message ChatMessage
	// Usage reports token counts for this exchange.
	Usage Usage
	// FinishReason names why generation stopped (e.g. "stop", "length",
	// "tool_call"); a driver that cannot determine one reports "".
	FinishReason string
}

// ModelEmbedRequest is the input to ModelProvider.Embed: the model-side
// embedding verb J/S-19.T6's api-backed Embedder rides on. It is distinct
// from the retrieval-side Embedder (embedder.go) - that seam stays
// F/S-10.T5's; this type exists only so a ModelProvider driver can expose
// its own embedding endpoint through the same five-verb contract.
type ModelEmbedRequest struct {
	// Inputs is the batch of texts to embed, in order.
	Inputs []string
	// Model optionally names the embedding model; empty defers to the
	// driver's own default.
	Model string
}

// ModelEmbedResponse is the result of ModelProvider.Embed: one vector per
// ModelEmbedRequest.Inputs entry, in the same order.
type ModelEmbedResponse struct {
	// Vectors holds one embedding per input, positionally corresponding to
	// ModelEmbedRequest.Inputs.
	Vectors [][]float32
	// Usage reports token counts for the batch.
	Usage Usage
}

// CountRequest is the input to ModelProvider.Count: the provider-side token
// count for a piece of text, as that provider's own tokenizer would count
// it (contrast NaiveTokenCounter's universal rune-based approximation).
type CountRequest struct {
	// Text is the content to count.
	Text string
	// Model optionally names which model's tokenizer to count under; empty
	// defers to the driver's own default.
	Model string
}

// CountResponse is the result of ModelProvider.Count.
type CountResponse struct {
	// Tokens is the provider's own token count for the requested text.
	Tokens int
}

// CapabilityState is a tri-state flag for one R-14.88 tool-capability
// dimension: a lane may support it, not support it, or its support may be
// unresolved. The zero value is CapabilityUnknown - an honest "not yet
// probed" default, distinct from an explicit CapabilityUnsupported.
type CapabilityState uint8

// The three CapabilityState members.
const (
	// CapabilityUnknown is the zero value: support has not been probed or
	// cannot currently be determined.
	CapabilityUnknown CapabilityState = iota
	// CapabilitySupported means the lane has been confirmed to support the
	// dimension.
	CapabilitySupported
	// CapabilityUnsupported means the lane has been confirmed NOT to
	// support the dimension.
	CapabilityUnsupported
)

// capabilityStateNames is indexed by CapabilityState value; String uses it
// in place of a switch so the exhaustive linter never applies here.
var capabilityStateNames = [...]string{"unknown", "supported", "unsupported"}

// Valid reports whether s is one of the three declared CapabilityState
// members.
func (s CapabilityState) Valid() bool {
	return s <= CapabilityUnsupported
}

// String returns the state's stable lowercase name, or "invalid-capability-
// state" for a value outside the declared set.
func (s CapabilityState) String() string {
	if !s.Valid() {
		return "invalid-capability-state"
	}
	return capabilityStateNames[s]
}

// RequiredCapabilities mirrors Capabilities' six R-14.88 tool-capability
// dimensions as a required-capability set: a caller sets the fields it
// needs to true, and the router (K/S-22.T2) excludes any candidate lane
// whose Capabilities does not resolve the matching field to
// CapabilitySupported. Description lives in Capabilities; this type never
// performs the matching itself.
type RequiredCapabilities struct {
	Search           bool
	URLFetch         bool
	Vision           bool
	ToolUse          bool
	LongContext      bool
	StructuredOutput bool
}

// Capabilities is the provider-side descriptor for one lane: the R-14.88
// tool-capability dimensions (each tri-state and resolvable per-lane, since
// pool members' tiers differ per key) plus the R-16.10 compliance posture
// every J/S-19 driver must fill. Capabilities describes a lane; it never
// routes - matching requirements against it is K/S-22.T2's job.
type Capabilities struct {
	Search           CapabilityState
	URLFetch         CapabilityState
	Vision           CapabilityState
	ToolUse          CapabilityState
	LongContext      CapabilityState
	StructuredOutput CapabilityState
	// CompliancePosture is the R-16.10 posture this lane operates under.
	// credential_sharing is always CredentialSharingForbidden;
	// NewCompliancePosture is the only constructor and enforces this.
	CompliancePosture CompliancePosture
}

// Satisfies reports whether c resolves every dimension req sets to true as
// CapabilitySupported. An unset (false) req field is never checked, so a
// caller that needs nothing always gets true regardless of c's state.
func (c Capabilities) Satisfies(req RequiredCapabilities) bool {
	if req.Search && c.Search != CapabilitySupported {
		return false
	}
	if req.URLFetch && c.URLFetch != CapabilitySupported {
		return false
	}
	if req.Vision && c.Vision != CapabilitySupported {
		return false
	}
	if req.ToolUse && c.ToolUse != CapabilitySupported {
		return false
	}
	if req.LongContext && c.LongContext != CapabilitySupported {
		return false
	}
	if req.StructuredOutput && c.StructuredOutput != CapabilitySupported {
		return false
	}
	return true
}

// StreamSink receives one StreamEvent at a time from ModelProvider.Stream.
// Returning a non-nil error aborts the stream; Stream returns that error to
// its own caller. A sink must not retain req across calls and must not
// block indefinitely - ctx cancellation is the caller's only lever to stop
// a slow sink.
type StreamSink func(StreamEvent) error

// ModelProvider is the vendor-neutral driver contract: exactly five verbs -
// chat, embed, count, stream, capabilities - and no sixth surface (06 §5
// rule 11; amended only by the not-yet-implemented R-21.28 Discover leg).
// Every crossing method takes ctx first and stores no ctx anywhere.
type ModelProvider interface {
	// Chat completes req as a single, non-streaming exchange.
	Chat(ctx context.Context, req ChatRequest) (ChatResponse, error)
	// Embed returns one vector per req.Inputs entry, in order.
	Embed(ctx context.Context, req ModelEmbedRequest) (ModelEmbedResponse, error)
	// Count returns the provider's own token count for req.Text.
	Count(ctx context.Context, req CountRequest) (CountResponse, error)
	// Stream completes req as a sequence of typed events delivered to sink,
	// in order, terminating in exactly one done or error event.
	Stream(ctx context.Context, req ChatRequest, sink StreamSink) error
	// Capabilities describes the named lane's current tool-capability
	// support and compliance posture. lane is a per-lane identifier
	// (capabilities are resolvable per-lane, since pool members' tiers
	// differ per key); an empty lane asks for the driver's own default.
	Capabilities(ctx context.Context, lane string) (Capabilities, error)
}
