// Purpose: the runnable godoc Example for provider.AgentProvider (Art.10.6)
//
//	and error-path tests over its shape.
//
// Constraints: every double here exists ONLY in this _test.go file
//
//	(Art.1.1); no implementation ships from this ticket — the plugin-hosted
//	AgentProvider driver is O/S-32.T1's wazero host, N/S-29's dispatch, and
//	R/S-40's routing to build.
//
// SPORT: pkg.provider.AgentProvider tests (ADD) — P1-E15-W4-S33-T1.
package provider_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// echoAgentProvider is a contract-holding AgentProvider double: Chat echoes
// the last message back, Embed/Count/Stream/Capabilities return fixed
// deterministic shapes.
type echoAgentProvider struct{}

var _ provider.AgentProvider = echoAgentProvider{}

func (echoAgentProvider) Chat(_ context.Context, req provider.ChatRequest) (provider.ChatResponse, error) {
	if len(req.Messages) == 0 {
		return provider.ChatResponse{}, cascade.New(cascade.KindInvalidInput, "chat request has no messages")
	}
	last := req.Messages[len(req.Messages)-1]
	return provider.ChatResponse{
		Message:      provider.ChatMessage{Role: "assistant", Content: "echo: " + last.Content},
		FinishReason: "stop",
	}, nil
}

func (echoAgentProvider) Embed(_ context.Context, req provider.ModelEmbedRequest) (provider.ModelEmbedResponse, error) {
	vectors := make([][]float32, len(req.Inputs))
	for i := range req.Inputs {
		vectors[i] = []float32{float32(len(req.Inputs[i]))}
	}
	return provider.ModelEmbedResponse{Vectors: vectors}, nil
}

func (echoAgentProvider) Count(_ context.Context, req provider.CountRequest) (provider.CountResponse, error) {
	return provider.CountResponse{Tokens: len(req.Text)}, nil
}

func (echoAgentProvider) Stream(_ context.Context, _ provider.ChatRequest, sink provider.StreamSink) error {
	return sink(provider.StreamEvent{Kind: provider.StreamEventDone})
}

func (echoAgentProvider) Capabilities(_ context.Context, _ string) (provider.Capabilities, error) {
	return provider.Capabilities{
		CompliancePosture: provider.NewCompliancePosture(
			[]string{"api-key"}, false, true, []string{"agent"}, "steady", false,
		),
	}, nil
}

// erroringAgentProvider always fails Chat with a taxonomy error, for the
// error-path test.
type erroringAgentProvider struct{ echoAgentProvider }

func (erroringAgentProvider) Chat(_ context.Context, _ provider.ChatRequest) (provider.ChatResponse, error) {
	return provider.ChatResponse{}, cascade.New(cascade.KindUnavailable, "agent provider offline")
}

func TestAgentProvider_ChatEmptyMessages(t *testing.T) {
	var ap provider.AgentProvider = echoAgentProvider{}
	_, err := ap.Chat(context.Background(), provider.ChatRequest{})
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("Chat with no messages: err = %v, want KindInvalidInput", err)
	}
}

func TestAgentProvider_ChatError(t *testing.T) {
	var ap provider.AgentProvider = erroringAgentProvider{}
	_, err := ap.Chat(context.Background(), provider.ChatRequest{
		Messages: []provider.ChatMessage{{Role: "user", Content: "hi"}},
	})
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("Chat error = %v, want KindUnavailable", err)
	}
}

func TestAgentProvider_EmbedCount(t *testing.T) {
	var ap provider.AgentProvider = echoAgentProvider{}
	ctx := context.Background()

	embedRes, err := ap.Embed(ctx, provider.ModelEmbedRequest{Inputs: []string{"ab", "abcd"}})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(embedRes.Vectors) != 2 || embedRes.Vectors[0][0] != 2 || embedRes.Vectors[1][0] != 4 {
		t.Fatalf("Embed vectors = %v, want lengths [2 4]", embedRes.Vectors)
	}

	countRes, err := ap.Count(ctx, provider.CountRequest{Text: "hello"})
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if countRes.Tokens != 5 {
		t.Fatalf("Count.Tokens = %d, want 5", countRes.Tokens)
	}
}

// ExampleAgentProvider dispatches a chat exchange through an AgentProvider
// implementation — the same shape a plugin's guest-side driver satisfies
// per pkg/plugin's guest invoke shim (R-14.50).
func ExampleAgentProvider() {
	ctx := context.Background()
	var ap provider.AgentProvider = echoAgentProvider{}

	res, err := ap.Chat(ctx, provider.ChatRequest{
		Messages: []provider.ChatMessage{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		fmt.Println("chat error:", err)
		return
	}

	fmt.Println(res.Message.Content)
	// Output: echo: hello
}
