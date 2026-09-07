//go:build integration

// Purpose: the tagged live lane against the real Anthropic API (Art.2's
//   "never a self-authored dialect" proof for this driver). Excluded from
//   the default unit lane by the integration build tag, so this file - and
//   only this file - may import "net"/"net/http" (Art.7.2).
// Constraints: credentials via env-ref only (ANTHROPIC_API_KEY); without
//   it, the test reports an explicit skip reason and never silently passes
//   as a real-counterpart proof.
// SPORT: placeholder: providers/anthropic driver (ADD) - see anthropic.go.

package anthropic

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/provider"
)

// liveKeyResolver hands back the one env-sourced key it was constructed
// with; production key resolution goes through the vault broker (out of
// this package's reach), and this integration lane only needs enough of
// that seam to exercise the real API once.
type liveKeyResolver string

func (r liveKeyResolver) Resolve(context.Context, string) (string, error) { return string(r), nil }

// liveDoer adapts *http.Client to this package's HTTPDoer, translating
// HTTPRequest/HTTPResponse to and from net/http's types. This adapter is
// legitimate here only because the file carries the integration tag.
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
	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		t.Skip("skip: ANTHROPIC_API_KEY is not set; this lane proves the real-counterpart claim and never substitutes a fake for it")
	}
	d, err := New(Config{
		Doer:  liveDoer{client: &http.Client{Timeout: 30 * time.Second}},
		Clock: fixedClock(time.Now()),
		Auth:  AuthConfig{Mode: AuthModeKey, KeyRef: "env", Resolver: liveKeyResolver(key)},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return d
}

// TestAnthropicDriverLiveAPI exercises Chat and Stream end-to-end against
// the real Anthropic API.
func TestAnthropicDriverLiveAPI(t *testing.T) {
	d := liveDriver(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	t.Run("chat", func(t *testing.T) {
		resp, err := d.Chat(ctx, provider.ChatRequest{
			Messages:        []provider.ChatMessage{{Role: "user", Content: "Reply with exactly one word: pong"}},
			MaxOutputTokens: 16,
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
			Messages:        []provider.ChatMessage{{Role: "user", Content: "Count from one to three."}},
			MaxOutputTokens: 32,
		}, func(ev provider.StreamEvent) error {
			switch ev.Kind {
			case provider.StreamEventDelta:
				deltas = append(deltas, ev.Delta)
			case provider.StreamEventDone:
				done = true
			case provider.StreamEventError:
				t.Fatalf("stream error event: %v", ev.Err)
			case provider.StreamEventUnknown, provider.StreamEventToolCall, provider.StreamEventUsage:
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
