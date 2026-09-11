// Purpose: edge-case tests for the registry-client types in registry.go —
//
//	real double implementations of RegistryFetcher, RegistryVerifier, and
//	RegistryCache that hold and break the fetch/verify/cache contract, per
//	Art.1.1 (every double lives only in this _test.go file; no
//	implementation ships from this ticket — X/S-50.T1 owns the live one).
//
// SPORT: pkg/plugin registry-client-types tests (ADD) — P1-E15-W4-S33-T1.
package plugin_test

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
)

// memFetcher is a RegistryFetcher double backed by an in-memory index and
// a set of named artifacts, plus an optional forced error for both verbs.
type memFetcher struct {
	index     []byte
	artifacts map[string][]byte
	failErr   error
}

var _ plugin.RegistryFetcher = memFetcher{}

func (f memFetcher) FetchIndex(_ context.Context) ([]byte, error) {
	if f.failErr != nil {
		return nil, f.failErr
	}
	return f.index, nil
}

func (f memFetcher) FetchArtifact(_ context.Context, entry plugin.RegistryVersionEntry) ([]byte, error) {
	if f.failErr != nil {
		return nil, f.failErr
	}
	data, ok := f.artifacts[entry.DownloadURL]
	if !ok {
		return nil, cascade.Newf(cascade.KindNotFound, "no artifact at %s", entry.DownloadURL)
	}
	return data, nil
}

// strictVerifier is a RegistryVerifier double that only accepts an exact
// checksum/signature match, failing closed with KindIntegrity otherwise.
type strictVerifier struct {
	wantIndex plugin.RegistryIndex
}

var _ plugin.RegistryVerifier = strictVerifier{}

func (v strictVerifier) VerifyIndex(_ context.Context, data []byte) (plugin.RegistryIndex, error) {
	if len(data) == 0 {
		return plugin.RegistryIndex{}, cascade.New(cascade.KindIntegrity, "empty index document")
	}
	return v.wantIndex, nil
}

func (v strictVerifier) VerifyArtifact(_ context.Context, _ []byte, entry plugin.RegistryVersionEntry) error {
	if entry.Checksum == "" {
		return cascade.New(cascade.KindIntegrity, "missing checksum")
	}
	return nil
}

// memCache is a RegistryCache double backed by a plain map.
type memCache struct {
	data map[string][]byte
}

var _ plugin.RegistryCache = (*memCache)(nil)

func (c *memCache) Get(_ context.Context, key string) ([]byte, bool, error) {
	v, ok := c.data[key]
	return v, ok, nil
}

func (c *memCache) Put(_ context.Context, key string, data []byte) error {
	c.data[key] = data
	return nil
}

func TestRegistryFetcher_FetchIndex(t *testing.T) {
	f := memFetcher{index: []byte(`{"schema_version":"v1"}`)}
	got, err := f.FetchIndex(context.Background())
	if err != nil {
		t.Fatalf("FetchIndex: %v", err)
	}
	if string(got) != `{"schema_version":"v1"}` {
		t.Fatalf("FetchIndex = %q, want the fixture index", got)
	}
}

func TestRegistryFetcher_FetchIndexError(t *testing.T) {
	f := memFetcher{failErr: cascade.New(cascade.KindUnavailable, "network down")}
	if _, err := f.FetchIndex(context.Background()); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("FetchIndex error = %v, want KindUnavailable", err)
	}
}

func TestRegistryFetcher_FetchArtifactNotFound(t *testing.T) {
	f := memFetcher{artifacts: map[string][]byte{}}
	entry := plugin.RegistryVersionEntry{DownloadURL: "https://example.invalid/missing.wasm"}
	if _, err := f.FetchArtifact(context.Background(), entry); !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("FetchArtifact error = %v, want KindNotFound", err)
	}
}

func TestRegistryVerifier_VerifyIndexFailsClosedOnEmpty(t *testing.T) {
	v := strictVerifier{}
	if _, err := v.VerifyIndex(context.Background(), nil); !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("VerifyIndex(nil) error = %v, want KindIntegrity", err)
	}
}

func TestRegistryVerifier_VerifyArtifactFailsClosedOnMissingChecksum(t *testing.T) {
	v := strictVerifier{}
	entry := plugin.RegistryVersionEntry{DownloadURL: "https://example.invalid/a.wasm"}
	if err := v.VerifyArtifact(context.Background(), []byte("data"), entry); !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("VerifyArtifact error = %v, want KindIntegrity", err)
	}
}

func TestRegistryCache_GetPutRoundTrip(t *testing.T) {
	c := &memCache{data: make(map[string][]byte)}
	ctx := context.Background()

	if _, ok, err := c.Get(ctx, "missing"); err != nil || ok {
		t.Fatalf("Get(missing) = ok=%v err=%v, want ok=false err=nil", ok, err)
	}

	if err := c.Put(ctx, "example@0.1.0", []byte("artifact-bytes")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, ok, err := c.Get(ctx, "example@0.1.0")
	if err != nil || !ok {
		t.Fatalf("Get(example@0.1.0) = ok=%v err=%v, want ok=true err=nil", ok, err)
	}
	if string(got) != "artifact-bytes" {
		t.Fatalf("Get(example@0.1.0) = %q, want %q", got, "artifact-bytes")
	}
}
