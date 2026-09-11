package provider

// Purpose: AgentProvider (R-14.50, AD/S-61.T1) — the contract a plugin
//
//	satisfying agent dispatch must implement, feeding N/S-29 and R/S-40
//	model routing. It is the exact same five-verb shape as ModelProvider
//	(model.go/types.go): chat, embed, count, stream, capabilities — no
//	sixth (06-FORGE-SPEC.md §5 rule 11) — because from the router's
//	perspective a plugin-hosted agent and an api-backed model driver are
//	interchangeable dispatch targets. compliance.go's CompliancePosture
//	applies to AgentProvider exactly as it does to ModelProvider (R-16.10):
//	both publish it through their Capabilities descriptor.
//
// Inputs: none at this layer — a contract, not behavior.
// Outputs: none.
// Constraints: pkg/provider imports nothing from internal/ (Art.10.2); no
//
//	duplicate types — reuses ChatRequest/ChatResponse/ModelEmbedRequest/
//	ModelEmbedResponse/CountRequest/CountResponse/StreamSink/Capabilities
//	from model.go and types.go rather than declaring parallel ones.
//
// SPORT: pkg.provider.AgentProvider/ADD (P1-E15-W4-S33-T1).

import "context"

// AgentProvider is the contract a plugin satisfying agent dispatch must
// implement. Its five methods mirror ModelProvider's exactly: a plugin
// author's guest-side implementation (see pkg/plugin's guest invoke shim,
// R-14.50) forwards each call through the plugin_invoke wasm export using
// the AgentProviderMethod binding named on that method's manifest
// provides entry.
type AgentProvider interface {
	// Chat completes req as a single, non-streaming exchange.
	Chat(ctx context.Context, req ChatRequest) (ChatResponse, error)
	// Embed returns one vector per req.Inputs entry, in order.
	Embed(ctx context.Context, req ModelEmbedRequest) (ModelEmbedResponse, error)
	// Count returns the plugin's own token count for req.Text.
	Count(ctx context.Context, req CountRequest) (CountResponse, error)
	// Stream completes req as a sequence of typed events delivered to
	// sink, in order, terminating in exactly one done or error event.
	Stream(ctx context.Context, req ChatRequest, sink StreamSink) error
	// Capabilities describes the named lane's current tool-capability
	// support and compliance posture, exactly as ModelProvider.Capabilities
	// does for an api-backed driver.
	Capabilities(ctx context.Context, lane string) (Capabilities, error)
}
