// Command example-agent-provider is the first-party example of a WASM guest
// binding AgentProvider semantics through the S-33.T1 guest shim per
// R-14.50 (02-TARGET-STRUCTURE.md §First-party plugin catalog v1:
// examples/example-agent-provider, wasm, off).
//
// Purpose: exercise plugin.GuestDispatcher end to end — a single
//
//	`plugin_invoke` WASM export, routing a JSON InvokeEnvelope to real
//	handlers for all five AgentProviderMethod verbs (chat, embed, count,
//	stream, capabilities) — the exact subset guestshim.go documents as the
//	WASM binding's scope. This plugin deliberately implements only that
//	five-verb subset, not pkg/provider.AgentProvider's full job-dispatch
//	surface (Spawn/Cancel/Collect/...): those verbs assume a supervised
//	child process with its own process group, which a WASM sandbox cannot
//	provide, so binding them here would not be real behavior — see this
//	ticket's journal DECISIONS section.
//
// Inputs: a JSON InvokeEnvelope (plugin.InvokeEnvelope) written into the
//
//	guest's own linear memory by the host before calling plugin_invoke.
//
// Outputs: a JSON InvokeResult (plugin.InvokeResult) written back into the
//
//	guest's own linear memory; plugin_invoke returns its byte length.
//
// Constraints: imports pkg/** only, never internal/** (Art.10.2); must
//
//	compile under GOOS=wasip1 GOARCH=wasm; the plugin_invoke export uses the
//	wasmexport compiler directive (Go 1.24+ wasip1 reactor support — built
//	with -buildmode=c-shared per the host toolchain's documented requirement
//	for a callable, non-auto-exiting wasip1 module); no bare
//	fmt.Errorf/errors.New (boundary lint); no panics — every handler
//	returns its failure as an error, which GuestDispatcher.Dispatch
//	carries in the returned envelope's Error field, never as a Go panic
//	crossing the WASM boundary. This file holds the host-portable dispatcher
//	and handlers (compiled and tested on every platform); the WASM-only
//	plugin_invoke export and its raw linear-memory pointer arithmetic live
//	in wasm_invoke.go behind a `//go:build wasip1` constraint, so `go vet`'s
//	unsafeptr check never fires on darwin/linux/windows for code that is
//	only valid inside a wasip1 guest's own address space (see
//	FIX-wasm-example-vet-portability journal).
//
// SPORT: plugins/examples/example-agent-provider (ADD) — P1-E15-W4-S33-T2.
package main

import (
	"context"
	"encoding/json"
	"unicode/utf8"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
	"github.com/acamarata/cascade/pkg/provider"
)

// dispatcher is built once at package init: every AgentProviderMethod verb
// this plugin declares in manifest.toml's provides.tools gets a real,
// registered handler below — no unregistered verb the manifest advertises.
var dispatcher = buildDispatcher()

func buildDispatcher() *plugin.GuestDispatcher {
	d := plugin.NewGuestDispatcher()
	d.Register(plugin.MethodChat, handleChat)
	d.Register(plugin.MethodEmbed, handleEmbed)
	d.Register(plugin.MethodCount, handleCount)
	d.Register(plugin.MethodStream, handleStream)
	d.Register(plugin.MethodCapabilities, handleCapabilities)
	return d
}

// handleChat implements MethodChat: a real, deterministic echo reply (no
// vendor model behind this example), with a genuine token-count-shaped
// Usage computed from the input, never a mock zero value.
func handleChat(_ context.Context, params json.RawMessage) (json.RawMessage, error) {
	var req provider.ChatRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, cascade.Wrap(cascade.KindInvalidInput, err, "example-agent-provider: decode chat request")
	}
	if len(req.Messages) == 0 {
		return nil, cascade.New(cascade.KindInvalidInput, "example-agent-provider: chat request has no messages")
	}
	last := req.Messages[len(req.Messages)-1]
	in := utf8.RuneCountInString(last.Content)
	resp := provider.ChatResponse{
		Message:      provider.ChatMessage{Role: "assistant", Content: "echo: " + last.Content},
		Usage:        provider.Usage{InputTokens: in, OutputTokens: in + 2},
		FinishReason: "stop",
	}
	return json.Marshal(resp)
}

// handleEmbed implements MethodEmbed: one deterministic 2-dimensional
// vector per input, derived from that input's own rune count and byte
// length so distinct inputs genuinely produce distinct vectors.
func handleEmbed(_ context.Context, params json.RawMessage) (json.RawMessage, error) {
	var req provider.ModelEmbedRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, cascade.Wrap(cascade.KindInvalidInput, err, "example-agent-provider: decode embed request")
	}
	if len(req.Inputs) == 0 {
		return nil, cascade.New(cascade.KindInvalidInput, "example-agent-provider: embed request has no inputs")
	}
	vectors := make([][]float32, len(req.Inputs))
	total := 0
	for i, in := range req.Inputs {
		runes := utf8.RuneCountInString(in)
		vectors[i] = []float32{float32(runes), float32(len(in))}
		total += runes
	}
	resp := provider.ModelEmbedResponse{
		Vectors: vectors,
		Usage:   provider.Usage{InputTokens: total},
	}
	return json.Marshal(resp)
}

// handleCount implements MethodCount using the same real rune-counting
// approach handleChat/handleEmbed use, refusing empty text rather than
// silently reporting a zero count for malformed input.
func handleCount(_ context.Context, params json.RawMessage) (json.RawMessage, error) {
	var req provider.CountRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, cascade.Wrap(cascade.KindInvalidInput, err, "example-agent-provider: decode count request")
	}
	if req.Text == "" {
		return nil, cascade.New(cascade.KindInvalidInput, "example-agent-provider: count request text must not be empty")
	}
	resp := provider.CountResponse{Tokens: utf8.RuneCountInString(req.Text)}
	return json.Marshal(resp)
}

// streamResult is this plugin's own wire shape for a buffered MethodStream
// response: provider.StreamSink is a Go func value and cannot cross the
// JSON envelope, so a WASM guest's single-call plugin_invoke buffers the
// full event sequence into one ordered slice instead of pushing to a live
// sink. This is the honest WASM-shaped equivalent of streaming, not a
// stand-in for it.
type streamResult struct {
	Events []provider.StreamEvent `json:"events"`
}

// handleStream implements MethodStream: one real StreamEventDelta per
// message (echoing its content, matching handleChat's own echo behavior)
// followed by exactly one terminal StreamEventDone, per the ModelProvider
// contract's "exactly one terminal event" rule.
func handleStream(_ context.Context, params json.RawMessage) (json.RawMessage, error) {
	var req provider.ChatRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, cascade.Wrap(cascade.KindInvalidInput, err, "example-agent-provider: decode stream request")
	}
	if len(req.Messages) == 0 {
		return nil, cascade.New(cascade.KindInvalidInput, "example-agent-provider: stream request has no messages")
	}
	events := make([]provider.StreamEvent, 0, len(req.Messages)+1)
	total := 0
	for _, msg := range req.Messages {
		events = append(events, provider.StreamEvent{Kind: provider.StreamEventDelta, Delta: "echo: " + msg.Content})
		total += utf8.RuneCountInString(msg.Content)
	}
	events = append(events, provider.StreamEvent{
		Kind:  provider.StreamEventDone,
		Usage: provider.Usage{InputTokens: total, OutputTokens: total},
	})
	return json.Marshal(streamResult{Events: events})
}

// handleCapabilities implements MethodCapabilities: a real, fixed
// descriptor (this example supports structured output only) with a valid
// CompliancePosture built through NewCompliancePosture — never a hand-built
// struct literal that could carry an invalid CredentialSharing value.
func handleCapabilities(_ context.Context, _ json.RawMessage) (json.RawMessage, error) {
	posture := provider.NewCompliancePosture(
		[]string{"none"}, false, true, []string{"agent"}, "steady", false,
	)
	caps := provider.Capabilities{
		Search:            provider.CapabilityUnsupported,
		URLFetch:          provider.CapabilityUnsupported,
		Vision:            provider.CapabilityUnsupported,
		ToolUse:           provider.CapabilityUnsupported,
		LongContext:       provider.CapabilityUnsupported,
		StructuredOutput:  provider.CapabilitySupported,
		CompliancePosture: posture,
	}
	return json.Marshal(caps)
}

// main is required by every build of this package (including GOOS=wasip1's
// -buildmode=c-shared reactor mode) but runs no host-ABI wiring itself:
// under wasip1, the real entry point is wasm_invoke.go's plugin_invoke
// WASM export; on every other platform this package builds host-side
// tests against but is never a runnable guest. Every real code path is
// reached through dispatcher, exercised by agent_test.go (in-process) and,
// on wasip1, by plugins/examples/integration_test.go's real wazero round
// trip. This mirrors example-domain/main.go's own "intentionally inert"
// main.
func main() {}
