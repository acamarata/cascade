package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/acamarata/cascade/pkg/plugin"
	"github.com/acamarata/cascade/pkg/provider"
)

func dispatchJSON(t *testing.T, method plugin.AgentProviderMethod, params any) plugin.InvokeResult {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	envelope, err := json.Marshal(plugin.InvokeEnvelope{Method: method.String(), Params: raw})
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	out := dispatcher.Dispatch(context.Background(), envelope)
	var res plugin.InvokeResult
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	return res
}

// TestHandleChat_Valid asserts a real echo reply with a genuine, non-zero
// Usage.
func TestHandleChat_Valid(t *testing.T) {
	res := dispatchJSON(t, plugin.MethodChat, provider.ChatRequest{
		Messages: []provider.ChatMessage{{Role: "user", Content: "hello"}},
	})
	if res.Error != nil {
		t.Fatalf("unexpected error: %+v", res.Error)
	}
	var resp provider.ChatResponse
	if err := json.Unmarshal(res.Result, &resp); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if resp.Message.Content != "echo: hello" {
		t.Fatalf("Message.Content = %q, want %q", resp.Message.Content, "echo: hello")
	}
	if resp.Usage.InputTokens == 0 {
		t.Fatal("Usage.InputTokens must be non-zero for non-empty input")
	}
}

// TestHandleChat_NoMessages is a required refusal case.
func TestHandleChat_NoMessages(t *testing.T) {
	res := dispatchJSON(t, plugin.MethodChat, provider.ChatRequest{})
	if res.Error == nil {
		t.Fatal("expected an InvokeError for a chat request with no messages")
	}
	if res.Error.Code != "handler-error" {
		t.Fatalf("Error.Code = %q, want %q", res.Error.Code, "handler-error")
	}
}

// TestHandleEmbed_Valid asserts one vector per input, with distinct inputs
// producing distinct vectors (never a fixed mock vector).
func TestHandleEmbed_Valid(t *testing.T) {
	res := dispatchJSON(t, plugin.MethodEmbed, provider.ModelEmbedRequest{Inputs: []string{"a", "bb"}})
	if res.Error != nil {
		t.Fatalf("unexpected error: %+v", res.Error)
	}
	var resp provider.ModelEmbedResponse
	if err := json.Unmarshal(res.Result, &resp); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if len(resp.Vectors) != 2 {
		t.Fatalf("len(Vectors) = %d, want 2", len(resp.Vectors))
	}
	if resp.Vectors[0][0] == resp.Vectors[1][0] {
		t.Fatal("distinct-length inputs must produce distinct vectors")
	}
}

// TestHandleEmbed_NoInputs is a required refusal case.
func TestHandleEmbed_NoInputs(t *testing.T) {
	res := dispatchJSON(t, plugin.MethodEmbed, provider.ModelEmbedRequest{})
	if res.Error == nil {
		t.Fatal("expected an InvokeError for an embed request with no inputs")
	}
}

// TestHandleCount_Valid asserts a real rune count, not a fixed mock value.
func TestHandleCount_Valid(t *testing.T) {
	res := dispatchJSON(t, plugin.MethodCount, provider.CountRequest{Text: "hello"})
	if res.Error != nil {
		t.Fatalf("unexpected error: %+v", res.Error)
	}
	var resp provider.CountResponse
	if err := json.Unmarshal(res.Result, &resp); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if resp.Tokens != 5 {
		t.Fatalf("Tokens = %d, want 5", resp.Tokens)
	}
}

// TestHandleCount_EmptyText is a required refusal case.
func TestHandleCount_EmptyText(t *testing.T) {
	res := dispatchJSON(t, plugin.MethodCount, provider.CountRequest{Text: ""})
	if res.Error == nil {
		t.Fatal("expected an InvokeError for an empty count request")
	}
}

// TestHandleStream_Valid asserts exactly one terminal StreamEventDone event
// following one StreamEventDelta per message.
func TestHandleStream_Valid(t *testing.T) {
	res := dispatchJSON(t, plugin.MethodStream, provider.ChatRequest{
		Messages: []provider.ChatMessage{{Role: "user", Content: "hi"}, {Role: "user", Content: "there"}},
	})
	if res.Error != nil {
		t.Fatalf("unexpected error: %+v", res.Error)
	}
	var resp streamResult
	if err := json.Unmarshal(res.Result, &resp); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if len(resp.Events) != 3 {
		t.Fatalf("len(Events) = %d, want 3 (2 deltas + 1 done)", len(resp.Events))
	}
	last := resp.Events[len(resp.Events)-1]
	if last.Kind != provider.StreamEventDone {
		t.Fatalf("last event Kind = %v, want %v", last.Kind, provider.StreamEventDone)
	}
}

// TestHandleStream_NoMessages is a required refusal case.
func TestHandleStream_NoMessages(t *testing.T) {
	res := dispatchJSON(t, plugin.MethodStream, provider.ChatRequest{})
	if res.Error == nil {
		t.Fatal("expected an InvokeError for a stream request with no messages")
	}
}

// TestHandleCapabilities_Valid asserts a valid, real Capabilities
// descriptor with a CompliancePosture that passes its own Validate.
func TestHandleCapabilities_Valid(t *testing.T) {
	res := dispatchJSON(t, plugin.MethodCapabilities, struct{}{})
	if res.Error != nil {
		t.Fatalf("unexpected error: %+v", res.Error)
	}
	var caps provider.Capabilities
	if err := json.Unmarshal(res.Result, &caps); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if caps.StructuredOutput != provider.CapabilitySupported {
		t.Fatalf("StructuredOutput = %v, want %v", caps.StructuredOutput, provider.CapabilitySupported)
	}
	if err := caps.CompliancePosture.Validate(); err != nil {
		t.Fatalf("CompliancePosture.Validate(): %v", err)
	}
}

// TestDispatcher_UnknownMethod is a required refusal case at the dispatcher
// boundary itself, not just inside one handler.
func TestDispatcher_UnknownMethod(t *testing.T) {
	envelope, err := json.Marshal(plugin.InvokeEnvelope{Method: "not-a-real-method", Params: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	out := dispatcher.Dispatch(context.Background(), envelope)
	var res plugin.InvokeResult
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if res.Error == nil || res.Error.Code != "unknown-method" {
		t.Fatalf("Error = %+v, want Code = %q", res.Error, "unknown-method")
	}
}

// TestDispatcher_MalformedEnvelope is a required refusal case for
// completely malformed input at the wire boundary.
func TestDispatcher_MalformedEnvelope(t *testing.T) {
	out := dispatcher.Dispatch(context.Background(), []byte("not json"))
	var res plugin.InvokeResult
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if res.Error == nil || res.Error.Code != "malformed-envelope" {
		t.Fatalf("Error = %+v, want Code = %q", res.Error, "malformed-envelope")
	}
}
