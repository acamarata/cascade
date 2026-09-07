//go:build integration

// Purpose: the tagged live lane (Art.2's real-counterpart proof for the
//
//	success path testdata/README.md documents as not-captured-live in the
//	unit lane): drives this driver's Chat and Stream against a real
//	openai-compat endpoint end-to-end. Credentials via env-ref only,
//	exactly like production wiring - never a literal here either.
//
// Constraints: this file alone in the package may import "net/http"; the
//
//	`integration` build tag excludes it from the default `go test
//	./providers/openai/` run entirely (Art.7.2).
//
// SPORT: providers/openai driver/ADD (P1-E10-W3-S19-T3).
package openai

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/provider"
)

// envResolver reads the named OS environment variable directly - the real
// shape of what the S-20.T1 intake's keychain-backed KeyResolver would
// return, without this test depending on the keychain being present in
// whatever CI runner executes the integration tag.
type envResolver struct{}

func (envResolver) Resolve(_ context.Context, keyRef string) (string, error) {
	return os.Getenv(keyRef), nil
}

// liveClock is a real Clock for this tagged lane only; internal/testkit is
// test-only and this package cannot import internal/runtime (providers ->
// pkg boundary), so the live lane's own local implementation is exactly as
// legitimate here as fixedClock is in the unit lane.
type liveClock struct{}

func (liveClock) Now() time.Time { return time.Now() }

// TestOpenAICompatDriverLiveAPI is this ticket's tagged live lane. Without
// CASCADE_OPENAI_TEST_KEY set it reports an explicit skip reason and never
// silently passes as a real-counterpart proof (Art.2). Set
// CASCADE_OPENAI_TEST_BASE_URL to point this at a zai/Moonshot/DeepSeek
// instance instead of the OpenAI default, and CASCADE_OPENAI_TEST_MODEL to
// match.
func TestOpenAICompatDriverLiveAPI(t *testing.T) {
	const keyRef = "CASCADE_OPENAI_TEST_KEY"
	if os.Getenv(keyRef) == "" {
		t.Skipf("skip: %s is not set; this lane requires a real openai-compat API key via env-ref and cannot prove a real counterpart without one", keyRef)
	}
	baseURL := os.Getenv("CASCADE_OPENAI_TEST_BASE_URL")
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	model := os.Getenv("CASCADE_OPENAI_TEST_MODEL")
	if model == "" {
		model = "gpt-4o-mini"
	}

	d, err := New(Config{
		BaseURL: baseURL, KeyRef: keyRef, Resolver: envResolver{},
		HTTPClient: &http.Client{Timeout: 30 * time.Second}, Clock: liveClock{},
		DefaultChatModel: model,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	t.Run("chat", func(t *testing.T) {
		resp, err := d.Chat(ctx, provider.ChatRequest{
			Messages:        []provider.ChatMessage{{Role: "user", Content: "Reply with exactly one word: pong"}},
			MaxOutputTokens: 8,
		})
		if err != nil {
			t.Fatalf("Chat: %v", err)
		}
		if resp.Message.Content == "" {
			t.Fatal("Chat: empty content from a real endpoint")
		}
	})

	t.Run("stream", func(t *testing.T) {
		var deltas int
		var sawDone bool
		err := d.Stream(ctx, provider.ChatRequest{
			Messages:        []provider.ChatMessage{{Role: "user", Content: "Count from one to three."}},
			MaxOutputTokens: 32,
		}, func(ev provider.StreamEvent) error {
			switch ev.Kind {
			case provider.StreamEventDelta:
				deltas++
			case provider.StreamEventDone:
				sawDone = true
			}
			return nil
		})
		if err != nil {
			t.Fatalf("Stream: %v", err)
		}
		if deltas == 0 || !sawDone {
			t.Fatalf("Stream: deltas=%d sawDone=%v against a real endpoint", deltas, sawDone)
		}
	})
}
