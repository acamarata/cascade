// Purpose: a runnable godoc Example for provider.ModelProvider (Art.10.6),
//
//	the five-verb driver contract J/S-19.T1 defines and every J/S-19 driver
//	(anthropic, openai-compat, gemini, ollama) implements. The wiki seed
//	(.github/wiki/Provider-Guide.md) cross-references this Example as the
//	concrete illustration of Section 1's interface walkthrough.
//
// Constraints: a real, minimal double (mirroring example_blob_test.go's
//
//	"why a local double" rationale) rather than a mock library import --
//	this package takes no test-double dependency.
//
// SPORT: pkg.provider.ModelProvider/ADDED (P1-E10-W3-S21-T4).
package provider_test

import (
	"context"
	"fmt"

	"github.com/acamarata/cascade/pkg/provider"
)

// exampleModelProvider is a minimal, real ModelProvider: it echoes the
// last user message back in upper case rather than calling out to any
// vendor API. It exists to demonstrate the five-verb contract's shape,
// not to be a realistic driver -- see .github/wiki/Provider-Guide.md
// Section 5 for the real driver walkthrough.
type exampleModelProvider struct{}

func (exampleModelProvider) Chat(_ context.Context, req provider.ChatRequest) (provider.ChatResponse, error) {
	last := ""
	if n := len(req.Messages); n > 0 {
		last = req.Messages[n-1].Content
	}
	return provider.ChatResponse{
		Message:      provider.ChatMessage{Role: "assistant", Content: upper(last)},
		FinishReason: "stop",
	}, nil
}

func (exampleModelProvider) Embed(_ context.Context, req provider.ModelEmbedRequest) (provider.ModelEmbedResponse, error) {
	vectors := make([][]float32, len(req.Inputs))
	for i := range req.Inputs {
		vectors[i] = []float32{float32(len(req.Inputs[i]))}
	}
	return provider.ModelEmbedResponse{Vectors: vectors}, nil
}

func (exampleModelProvider) Count(_ context.Context, req provider.CountRequest) (provider.CountResponse, error) {
	return provider.CountResponse{Tokens: len(req.Text)}, nil
}

func (exampleModelProvider) Stream(_ context.Context, _ provider.ChatRequest, sink provider.StreamSink) error {
	return sink(provider.StreamEvent{Kind: provider.StreamEventDone})
}

func (exampleModelProvider) Capabilities(_ context.Context, _ string) (provider.Capabilities, error) {
	return provider.Capabilities{}, nil
}

// upper is a tiny ASCII upper-caser so this example takes no strings
// import beyond what fmt already needs for the Output comment.
func upper(s string) string {
	out := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' {
			c -= 'a' - 'A'
		}
		out[i] = c
	}
	return string(out)
}

// Example demonstrates ModelProvider's Chat verb: every J/S-19 driver
// (anthropic, openai-compat, gemini, ollama) implements exactly this
// five-method interface, and a caller never needs to know which one it
// holds. See .github/wiki/Provider-Guide.md Section 1 for the full
// method-by-method contract and Section 5 for how to add a new driver.
func Example() {
	var mp provider.ModelProvider = exampleModelProvider{}

	resp, err := mp.Chat(context.Background(), provider.ChatRequest{
		Messages: []provider.ChatMessage{{Role: "user", Content: "hello cascade"}},
	})
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	fmt.Println(resp.Message.Content)
	// Output: HELLO CASCADE
}
