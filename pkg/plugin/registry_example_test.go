// Purpose: the runnable godoc Example for RegistryClient.Search
//
//	(12-QUALITY-CONSTITUTION.md Art.10 §4/§6 — every pkg/ entry point
//	needs a compiling, doc-visible Example). Uses the same real
//	Ed25519-signed fixture as registry_client_test.go (Art.2).
//
// SPORT: pkg/plugin registry-client (ADD) — P1-E24-W5-S50-T1.
package plugin_test

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/acamarata/cascade/pkg/plugin"
)

// exampleClock is a fixed Clock for this Example — determinism is not
// load-bearing here (no cache is used), but NewRegistryClient requires a
// non-nil Clock per its own doc comment.
type exampleClock struct{ t time.Time }

func (c exampleClock) Now() time.Time { return c.t }

// exampleFetcher serves one fixed, real, signed index document — a
// minimal stand-in for internal/plugins/registryfetch.HTTPFetcher, which
// this pkg/ package cannot import (pkg/ never imports internal/).
type exampleFetcher struct{ index []byte }

func (f exampleFetcher) FetchIndex(context.Context) ([]byte, error) { return f.index, nil }
func (f exampleFetcher) FetchArtifact(context.Context, plugin.RegistryVersionEntry) ([]byte, error) {
	return nil, nil
}

// ExampleRegistryClient_Search fetches, verifies, and searches the
// package's real fixture index for plugins tagged "linter".
func ExampleRegistryClient_Search() {
	data, err := os.ReadFile(filepath.Join("testdata", "registry", "index.json"))
	if err != nil {
		fmt.Println("read fixture error:", err)
		return
	}
	pubRaw, err := base64.StdEncoding.DecodeString("ANBaHR6iUTltVXr71FiLPG2Z2+uXL+0QoyVi6ibc3Po=")
	if err != nil {
		fmt.Println("decode key error:", err)
		return
	}

	client := plugin.NewRegistryClient(
		plugin.RegistryConfig{},
		exampleFetcher{index: data},
		plugin.Ed25519Verifier{PublicKey: ed25519.PublicKey(pubRaw)},
		nil,
		exampleClock{},
	)

	results, err := client.Search(context.Background(), "linter")
	if err != nil {
		fmt.Println("search error:", err)
		return
	}
	for _, r := range results {
		fmt.Println(r.ID)
	}
	// Output: example-linter
}
