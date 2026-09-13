package dispatch

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/providers/anthropic"
	"github.com/acamarata/cascade/providers/gemini"
	"github.com/acamarata/cascade/providers/ollama"
	"github.com/acamarata/cascade/providers/openai"
)

// TestResolve_BuildsRealDriverPerKind proves Resolve is a genuine adapter,
// not a fabricated success dressed up as one (Art.1): given a
// CredentialSource, it returns the actual providers/* driver type the
// registry's DriverKind names, for every kind this tree implements.
func TestResolve_BuildsRealDriverPerKind(t *testing.T) {
	cases := []struct {
		name   string
		driver registry.DriverKind
		check  func(t *testing.T, got provider.ModelProvider)
	}{
		{"anthropic", registry.DriverAnthropic, func(t *testing.T, got provider.ModelProvider) {
			if _, ok := got.(*anthropic.Driver); !ok {
				t.Fatalf("got %T, want *anthropic.Driver", got)
			}
		}},
		{"gemini", registry.DriverGemini, func(t *testing.T, got provider.ModelProvider) {
			if _, ok := got.(*gemini.Driver); !ok {
				t.Fatalf("got %T, want *gemini.Driver", got)
			}
		}},
		{"ollama", registry.DriverOllama, func(t *testing.T, got provider.ModelProvider) {
			if _, ok := got.(*ollama.Driver); !ok {
				t.Fatalf("got %T, want *ollama.Driver", got)
			}
		}},
		{"openai-compat", registry.DriverOpenAICompat, func(t *testing.T, got provider.ModelProvider) {
			if _, ok := got.(*openai.Driver); !ok {
				t.Fatalf("got %T, want *openai.Driver", got)
			}
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := keyProvider(tc.name+"-1", tc.driver)
			creds := &fakeCredentials{values: map[string]string{rec.AuthRef.String(): "secret-value"}}
			res, err := NewResolver(testLookup(rec), creds, runtime.SystemClock{}, &fakeTransport{})
			if err != nil {
				t.Fatalf("NewResolver: %v", err)
			}
			got, rerr := res.Resolve(context.Background(), provider.Selection{LaneID: "lane-1", Model: "test-model"})
			if rerr != nil {
				t.Fatalf("Resolve: %v", rerr)
			}
			if got == nil {
				t.Fatal("Resolve returned a nil ModelProvider with no error")
			}
			tc.check(t, got)
		})
	}
}
