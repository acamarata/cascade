//go:build integration

// Purpose: the tagged live lane against a real Ollama server (Art.2's
//   "never a self-authored dialect" proof for this driver). Excluded from
//   the default unit lane by the integration build tag, so this file - and
//   only this file - may import "net"/"net/http" (Art.7.2).
// Constraints: base_url via OLLAMA_BASE_URL only; without it, the test
//   reports an explicit skip reason and never silently passes as a
//   real-counterpart proof.
// SPORT: placeholder: providers/ollama driver (ADD) - see ollama.go.

package ollama

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/provider"
)

// liveDoer adapts *http.Client to this package's HTTPDoer, translating
// HTTPRequest/HTTPResponse to and from net/http's types. Legitimate here
// only because this file carries the integration tag.
type liveDoer struct{ client *http.Client }

func (d liveDoer) Do(ctx context.Context, req HTTPRequest) (HTTPResponse, error) {
	httpReq, err := http.NewRequestWithContext(ctx, req.Method, req.URL, bytes.NewReader(req.Body))
	if err != nil {
		return HTTPResponse{}, err
	}
	for k, v := range req.Headers {
		httpReq.Header.Set(k, v)
	}
	resp, err := d.client.Do(httpReq)
	if err != nil {
		return HTTPResponse{}, err
	}
	return HTTPResponse{Status: resp.StatusCode, Body: resp.Body}, nil
}

func liveDriver(t *testing.T) *Driver {
	t.Helper()
	base := os.Getenv("OLLAMA_BASE_URL")
	if base == "" {
		t.Skip("skip: OLLAMA_BASE_URL is not set; this lane proves the real-counterpart claim against a live Ollama server and never substitutes a fake for it")
	}
	d, err := New(Config{
		BaseURL: base,
		Doer:    liveDoer{client: &http.Client{Timeout: 60 * time.Second}},
		Clock:   fixedClock(time.Now()),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return d
}

// TestOllamaDriverLiveAPI exercises Chat and Stream end-to-end against a
// real Ollama server. It needs at least one model already pulled on that
// server (OLLAMA_MODEL, defaulting to "llama3.2") - pulling a model is
// outside this test's scope.
func TestOllamaDriverLiveAPI(t *testing.T) {
	d := liveDriver(t)
	model := os.Getenv("OLLAMA_MODEL")
	if model == "" {
		model = "llama3.2"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	t.Run("chat", func(t *testing.T) {
		resp, err := d.Chat(ctx, provider.ChatRequest{
			Model:    model,
			Messages: []provider.ChatMessage{{Role: "user", Content: "Reply with exactly one word: pong"}},
		})
		if err != nil {
			t.Fatalf("Chat: %v", err)
		}
		if resp.Message.Content == "" {
			t.Fatal("Chat returned an empty message")
		}
	})

	t.Run("stream", func(t *testing.T) {
		var deltas []string
		done := false
		err := d.Stream(ctx, provider.ChatRequest{
			Model:    model,
			Messages: []provider.ChatMessage{{Role: "user", Content: "Count from one to three."}},
		}, func(ev provider.StreamEvent) error {
			switch ev.Kind {
			case provider.StreamEventDelta:
				deltas = append(deltas, ev.Delta)
			case provider.StreamEventDone:
				done = true
			case provider.StreamEventUnknown, provider.StreamEventToolCall, provider.StreamEventUsage, provider.StreamEventError:
				// Not asserted by this smoke test.
			}
			return nil
		})
		if err != nil {
			t.Fatalf("Stream: %v", err)
		}
		if !done {
			t.Fatal("Stream never delivered a done event")
		}
		if len(deltas) == 0 {
			t.Fatal("Stream delivered no text deltas")
		}
	})
}
