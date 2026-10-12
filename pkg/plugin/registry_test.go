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
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"strings"
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

// TestLegacyIndexVerifiesButClientRefusesSequenceZero: an index signed in
// the pre-sequence format ({schema_version, entries} only) still verifies
// byte-for-byte through Ed25519Verifier and NewVerifiedIndex, and
// RegistryClient alone refuses it as sequence 0.
func TestLegacyIndexVerifiesButClientRefusesSequenceZero(t *testing.T) {
	legacy := signedSeqIndex(t, rollbackKey(), 0, "legacy")
	if strings.Contains(string(legacy), "sequence") || strings.Contains(string(legacy), "published_at") {
		t.Fatalf("legacy fixture carries the new fields: %s", legacy)
	}
	verifier := plugin.Ed25519Verifier{PublicKey: rollbackKey().Public().(ed25519.PublicKey)}
	idx, err := verifier.VerifyIndex(context.Background(), legacy)
	if err != nil || idx.Sequence != 0 || idx.PublishedAt != "" || len(idx.Entries) != 1 {
		t.Fatalf("VerifyIndex(legacy) = seq %d published %q entries %d, err %v; want a verified sequence-0 index", idx.Sequence, idx.PublishedAt, len(idx.Entries), err)
	}
	if vi, err := plugin.NewVerifiedIndex(context.Background(), verifier, legacy); err != nil || len(vi.Entries()) != 1 {
		t.Fatalf("NewVerifiedIndex(legacy) err = %v, want the verified witness", err)
	}
	c, dir := rollbackClient(t, &swapFetcher{index: legacy})
	assertRollbackRefusal(t, c, dir, "sequence 0")
}

// TestStrippedSequenceFailsSignature: deleting sequence and published_at
// from an index signed with them changes the signed payload.
func TestStrippedSequenceFailsSignature(t *testing.T) {
	data := realFixtureBytes(t)
	if _, err := realVerifier(t).VerifyIndex(context.Background(), data); err != nil {
		t.Fatalf("unstripped fixture must verify: %v", err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	if string(doc["sequence"]) != "1" {
		t.Fatalf("fixture sequence = %s, want 1", doc["sequence"])
	}
	delete(doc, "sequence")
	delete(doc, "published_at")
	stripped, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("encode stripped: %v", err)
	}
	_, err = realVerifier(t).VerifyIndex(context.Background(), stripped)
	var ce *cascade.Error
	if !errors.As(err, &ce) || ce.Kind != cascade.KindIntegrity || !strings.Contains(err.Error(), "signature does not verify") {
		t.Fatalf("VerifyIndex(stripped) err = %v, want KindIntegrity \"signature does not verify\"", err)
	}
}

// TestUpdateFixtureCarriesSequence: the re-signed grant-diff update
// fixture verifies under its pinned key and carries sequence 1.
func TestUpdateFixtureCarriesSequence(t *testing.T) {
	idx, err := grantDiffVerifier(t).VerifyIndex(context.Background(), grantDiffFixtureBytes(t))
	if err != nil {
		t.Fatalf("VerifyIndex(update fixture): %v", err)
	}
	if idx.Sequence != 1 || idx.PublishedAt != "2026-01-01T00:00:00Z" {
		t.Fatalf("update fixture = sequence %d published_at %q, want 1 / 2026-01-01T00:00:00Z", idx.Sequence, idx.PublishedAt)
	}
}
