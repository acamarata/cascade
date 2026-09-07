//go:build integration

// Purpose: the tagged live lane (Art.2's real-counterpart proof for the
//   end-to-end path): drives ProviderEmbedder over a real
//   provider.ModelProvider (the already-landed openai-compat driver)
//   against a real endpoint, when explicitly opted into.
// Constraints: this file alone in the package may import "net/http" - the
//   `integration` build tag excludes it from the default
//   `go test ./providers/embeddings/` run entirely (Art.7.2), matching
//   every sibling J/S-19 driver's own integration_test.go.
// SPORT: providers.embeddings.ProviderEmbedder/ADD (P1-E10-W3-S19-T6).

package embeddings

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/providers/openai"
)

// envResolver reads the named OS environment variable directly, matching
// the shape providers/openai's own integration test uses for the same
// reason: the real S-20.T1 keychain-backed resolver need not be present
// for this opt-in lane.
type envResolver struct{}

func (envResolver) Resolve(_ context.Context, keyRef string) (string, error) {
	return os.Getenv(keyRef), nil
}

// liveClock is a real Clock for this tagged lane only.
type liveClock struct{}

func (liveClock) Now() time.Time { return time.Now() }

// allowAllIntercept is an Interceptor that never rewrites or refuses. The
// K/S-22.T3 sensitivity seam itself is not this ticket's live-lane
// concern (R-21.282(b)); this lane proves ProviderEmbedder's real-provider
// round-trip, not the seam's own policy.
type allowAllIntercept struct{}

func (allowAllIntercept) InterceptClass(_ context.Context, _ EgressClass, _ provider.SensitivityTier, content []byte) ([]byte, error) {
	return content, nil
}

// TestProviderEmbedderLiveAPI is this ticket's tagged live lane. Without
// PROVIDER_EMBED_TEST=1 it reports an explicit skip reason and never
// silently passes as a real-counterpart proof (Art.2). It additionally
// needs a real openai-compat API key via CASCADE_OPENAI_TEST_KEY, exactly
// like providers/openai's own integration test, since ProviderEmbedder
// has no embedding backend of its own to call.
func TestProviderEmbedderLiveAPI(t *testing.T) {
	if os.Getenv("PROVIDER_EMBED_TEST") != "1" {
		t.Skip("skip: PROVIDER_EMBED_TEST is not 1; this lane calls a real provider end-to-end and cannot prove a real counterpart without opting in")
	}
	const keyRef = "CASCADE_OPENAI_TEST_KEY"
	if os.Getenv(keyRef) == "" {
		t.Skipf("skip: %s is not set; PROVIDER_EMBED_TEST=1 still needs a real credential to call a live embeddings endpoint", keyRef)
	}
	baseURL := os.Getenv("CASCADE_OPENAI_TEST_BASE_URL")
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	model := os.Getenv("CASCADE_OPENAI_TEST_EMBED_MODEL")
	if model == "" {
		model = "text-embedding-3-small"
	}

	driver, err := openai.New(openai.Config{
		BaseURL:           baseURL,
		KeyRef:            keyRef,
		Resolver:          envResolver{},
		HTTPClient:        http.DefaultClient,
		Clock:             liveClock{},
		DefaultEmbedModel: model,
	})
	if err != nil {
		t.Fatalf("openai.New: %v", err)
	}

	pe, err := NewProviderEmbedder(driver, provider.EmbedModel{ID: model, Dimensions: 1536}, provider.SensitivityInternal, allowAllIntercept{})
	if err != nil {
		t.Fatalf("NewProviderEmbedder: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	outputs, err := pe.Embed(ctx, []provider.EmbedInput{{Text: "cascade live embedding integration probe"}})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(outputs) != 1 || len(outputs[0].Vector) == 0 {
		t.Fatalf("outputs = %+v, want one non-empty vector", outputs)
	}
}
