// Purpose: end-to-end tests of RegistryClient over the real fixture at
//
//	testdata/registry/index.json (Art.2 — real Ed25519 counterpart, see
//	testdata/registry/README.md for generation provenance), plus the
//	error-path and cache-hit contracts the ticket's acceptance criteria
//	name explicitly.
//
// SPORT: pkg/plugin registry-client tests (ADD) — P1-E24-W5-S50-T1.
package plugin_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
)

func decodeTestPublicKey(t *testing.T) ed25519.PublicKey {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(testRegistryPublicKeyB64)
	if err != nil {
		t.Fatalf("decode test public key: %v", err)
	}
	return ed25519.PublicKey(raw)
}

// testRegistryPublicKeyB64 is the Ed25519 public key (base64, standard
// encoding) whose matching private key signed testdata/registry/index.json.
// Split across two literals only where needed elsewhere in this file to
// dodge credential-shape scanners; this key by itself has no such shape
// (see AGENT-BRIEF.md's push-protection note) so it is kept as one
// constant here for readability.
const testRegistryPublicKeyB64 = "ANBaHR6iUTltVXr71FiLPG2Z2+uXL+0QoyVi6ibc3Po="

func realFixtureBytes(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "registry", "index.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return data
}

func realVerifier(t *testing.T) plugin.Ed25519Verifier {
	t.Helper()
	pub := decodeTestPublicKey(t)
	return plugin.Ed25519Verifier{PublicKey: pub}
}

// fakeFetcher is a plugin.RegistryFetcher double that counts how many
// times FetchIndex is called, so tests can assert "zero HTTP calls" on a
// cache hit.
type fakeFetcher struct {
	index      []byte
	err        error
	fetchCalls int
}

func (f *fakeFetcher) FetchIndex(context.Context) ([]byte, error) {
	f.fetchCalls++
	if f.err != nil {
		return nil, f.err
	}
	return f.index, nil
}

func (f *fakeFetcher) FetchArtifact(context.Context, plugin.RegistryVersionEntry) ([]byte, error) {
	return nil, cascade.New(cascade.KindUnsupported, "not used by these tests")
}

// fakeClock is a plugin.Clock double with a settable instant.
type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

func TestFetchVerifiesSignature(t *testing.T) {
	data := realFixtureBytes(t)
	fetcher := &fakeFetcher{index: data}
	client := plugin.NewRegistryClient(plugin.RegistryConfig{}, fetcher, realVerifier(t), nil, &fakeClock{now: time.Unix(1000, 0)})

	idx, err := client.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if idx.SchemaVersion != plugin.SchemaVersionCurrent {
		t.Fatalf("SchemaVersion = %q, want %q", idx.SchemaVersion, plugin.SchemaVersionCurrent)
	}
	if len(idx.Entries) != 2 {
		t.Fatalf("Entries = %d, want 2", len(idx.Entries))
	}
	if idx.Entries[0].ID != "example-formatter" {
		t.Fatalf("Entries[0].ID = %q, want example-formatter", idx.Entries[0].ID)
	}
}

func TestFetchRejectsCorrupted(t *testing.T) {
	data := realFixtureBytes(t)
	// Mutate one character inside a signed value (the first entry's
	// display name) while keeping the document valid JSON, so the
	// failure exercised here is specifically signature mismatch, not
	// JSON parse failure.
	const original = "Example Formatter"
	const mutated = "Example Formattes"
	if !bytes.Contains(data, []byte(original)) {
		t.Fatalf("fixture no longer contains %q; update this test's mutation target", original)
	}
	corrupted := bytes.Replace(data, []byte(original), []byte(mutated), 1)

	fetcher := &fakeFetcher{index: corrupted}
	cache := &recordingCache{}
	client := plugin.NewRegistryClient(plugin.RegistryConfig{CacheTTL: time.Hour}, fetcher, realVerifier(t), cache, &fakeClock{now: time.Unix(1000, 0)})

	_, err := client.Fetch(context.Background())
	if !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("Fetch(corrupted) error = %v, want KindIntegrity", err)
	}
	if cache.puts != 0 {
		t.Fatalf("Fetch(corrupted) wrote %d cache entries, want 0 (never cache unverified payload)", cache.puts)
	}
}

// recordingCache is a plugin.RegistryCache double backed by a map, that
// also counts Put calls.
type recordingCache struct {
	data map[string][]byte
	puts int
}

func (c *recordingCache) Get(_ context.Context, key string) ([]byte, bool, error) {
	if c.data == nil {
		return nil, false, nil
	}
	v, ok := c.data[key]
	return v, ok, nil
}

func (c *recordingCache) Put(_ context.Context, key string, data []byte) error {
	if c.data == nil {
		c.data = make(map[string][]byte)
	}
	c.data[key] = data
	c.puts++
	return nil
}

func TestCacheHit(t *testing.T) {
	data := realFixtureBytes(t)
	fetcher := &fakeFetcher{index: data}
	cache := &recordingCache{}
	clock := &fakeClock{now: time.Unix(1000, 0)}
	client := plugin.NewRegistryClient(plugin.RegistryConfig{CacheTTL: time.Hour}, fetcher, realVerifier(t), cache, clock)

	if _, err := client.Fetch(context.Background()); err != nil {
		t.Fatalf("first Fetch: %v", err)
	}
	if fetcher.fetchCalls != 1 {
		t.Fatalf("after first Fetch, fetchCalls = %d, want 1", fetcher.fetchCalls)
	}

	// Second Fetch, clock advanced but still within TTL: must not call
	// FetchIndex again.
	clock.now = clock.now.Add(time.Minute)
	if _, err := client.Fetch(context.Background()); err != nil {
		t.Fatalf("second Fetch: %v", err)
	}
	if fetcher.fetchCalls != 1 {
		t.Fatalf("after second Fetch within TTL, fetchCalls = %d, want 1 (zero additional HTTP calls)", fetcher.fetchCalls)
	}

	// Third Fetch, clock advanced past TTL: must re-fetch.
	clock.now = clock.now.Add(2 * time.Hour)
	if _, err := client.Fetch(context.Background()); err != nil {
		t.Fatalf("third Fetch: %v", err)
	}
	if fetcher.fetchCalls != 2 {
		t.Fatalf("after third Fetch past TTL, fetchCalls = %d, want 2", fetcher.fetchCalls)
	}
}

func TestCacheHitRefusesCorruptedCacheEntry(t *testing.T) {
	data := realFixtureBytes(t)
	fetcher := &fakeFetcher{index: data}
	cache := &recordingCache{data: map[string][]byte{"index": []byte("not valid json at all")}}
	client := plugin.NewRegistryClient(plugin.RegistryConfig{CacheTTL: time.Hour}, fetcher, realVerifier(t), cache, &fakeClock{now: time.Unix(1000, 0)})

	idx, err := client.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch with corrupt cache entry: %v", err)
	}
	if fetcher.fetchCalls != 1 {
		t.Fatalf("Fetch with corrupt cache entry: fetchCalls = %d, want 1 (corrupt entry treated as miss, re-fetched)", fetcher.fetchCalls)
	}
	if len(idx.Entries) != 2 {
		t.Fatalf("Entries = %d, want 2", len(idx.Entries))
	}
}

func TestSearch(t *testing.T) {
	data := realFixtureBytes(t)
	client := plugin.NewRegistryClient(plugin.RegistryConfig{}, &fakeFetcher{index: data}, realVerifier(t), nil, &fakeClock{now: time.Unix(1000, 0)})

	all, err := client.Search(context.Background(), "")
	if err != nil {
		t.Fatalf("Search(\"\"): %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("Search(\"\") = %d entries, want 2 (browse mode)", len(all))
	}

	byName, err := client.Search(context.Background(), "FORMATTER")
	if err != nil {
		t.Fatalf("Search(FORMATTER): %v", err)
	}
	if len(byName) != 1 || byName[0].ID != "example-formatter" {
		t.Fatalf("Search(FORMATTER) = %+v, want just example-formatter", byName)
	}

	byTag, err := client.Search(context.Background(), "quality")
	if err != nil {
		t.Fatalf("Search(quality): %v", err)
	}
	if len(byTag) != 1 || byTag[0].ID != "example-linter" {
		t.Fatalf("Search(quality) = %+v, want just example-linter", byTag)
	}

	none, err := client.Search(context.Background(), "nonexistent-plugin-xyz")
	if err != nil {
		t.Fatalf("Search(nonexistent): %v", err)
	}
	if len(none) != 0 {
		t.Fatalf("Search(nonexistent) = %d entries, want 0", len(none))
	}
}

func TestFetch_ErrRegistryHTTPPropagates(t *testing.T) {
	fetcher := &fakeFetcher{err: cascade.Wrap(cascade.KindUnavailable, plugin.ErrRegistryHTTP, "GET https://registry.example.com/index.json: status 503")}
	client := plugin.NewRegistryClient(plugin.RegistryConfig{}, fetcher, realVerifier(t), nil, &fakeClock{now: time.Unix(1000, 0)})

	_, err := client.Fetch(context.Background())
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("Fetch with fetcher error = %v, want KindUnavailable (ErrRegistryHTTP)", err)
	}
}

func TestVerifyIndex_ErrIndexMalformedOnBadJSON(t *testing.T) {
	_, err := realVerifier(t).VerifyIndex(context.Background(), []byte("not json"))
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("VerifyIndex(bad json) error = %v, want KindInvalidInput (ErrIndexMalformed)", err)
	}
}

func TestVerifyIndex_ErrIndexMalformedOnUnsupportedSchemaVersion(t *testing.T) {
	_, err := realVerifier(t).VerifyIndex(context.Background(), []byte(`{"schema_version":"99","entries":[],"signature":"x"}`))
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("VerifyIndex(bad schema_version) error = %v, want KindInvalidInput (ErrIndexMalformed)", err)
	}
}

func TestVerifyIndex_ErrSignatureInvalidOnEmptySignature(t *testing.T) {
	_, err := realVerifier(t).VerifyIndex(context.Background(), []byte(`{"schema_version":"1","entries":[],"signature":""}`))
	if !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("VerifyIndex(empty signature) error = %v, want KindIntegrity (ErrSignatureInvalid)", err)
	}
}

func TestVerifyIndex_ErrSignatureInvalidOnGarbageSignature(t *testing.T) {
	_, err := realVerifier(t).VerifyIndex(context.Background(), []byte(`{"schema_version":"1","entries":[],"signature":"not-base64!!"}`))
	if !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("VerifyIndex(garbage signature) error = %v, want KindIntegrity (ErrSignatureInvalid)", err)
	}
}

func TestVerifyIndex_ErrSignatureInvalidOnUntrustedKey(t *testing.T) {
	data := realFixtureBytes(t)
	// A different, unrelated Ed25519 public key must never verify the
	// real fixture's signature.
	untrusted := plugin.Ed25519Verifier{PublicKey: make([]byte, 32)}
	_, err := untrusted.VerifyIndex(context.Background(), data)
	if !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("VerifyIndex with untrusted key error = %v, want KindIntegrity (ErrSignatureInvalid)", err)
	}
}

func TestVerifyIndex_ErrSignatureInvalidOnUnknownKeyLength(t *testing.T) {
	data := realFixtureBytes(t)
	badKey := plugin.Ed25519Verifier{PublicKey: []byte{1, 2, 3}}
	_, err := badKey.VerifyIndex(context.Background(), data)
	if !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("VerifyIndex with wrong-length key error = %v, want KindIntegrity", err)
	}
}
