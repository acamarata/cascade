// Purpose: RegistryClient rollback and replay refusal (registry_rollback.go):
//
//	an older signed index, the same sequence with different bytes and
//	sequence 0 are refused with KindIntegrity "registry index rollback"
//	and leave <CacheDir>/index.seq byte-identical; a newer index moves the
//	baseline; an index whose signature fails never touches the baseline.
//
// Inputs: indexes signed in-test with a fixed throwaway Ed25519 seed, a
//
//	t.TempDir() cache dir and a fixed clock. No network.
//
// Outputs: none (pass/fail only).
// SPORT: pkg/plugin registry-index-rollback tests (ADD).
package plugin_test

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
)

// rollbackKey is a fixed, test-only Ed25519 key (seed bytes 50..81); it
// signs nothing outside this file.
func rollbackKey() ed25519.PrivateKey {
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(50 + i)
	}
	return ed25519.NewKeyFromSeed(seed)
}

// signedSeqIndex builds and signs an index document. The signed payload is
// written out literally here (field order schema_version, entries,
// sequence, published_at; the last two omitted when zero) rather than taken
// from production code, so a drift in either side fails a test.
func signedSeqIndex(t *testing.T, priv ed25519.PrivateKey, seq uint64, name string) []byte {
	t.Helper()
	entries, err := json.Marshal([]plugin.RegistryIndexEntry{{ID: "demo", Name: name, LatestVersion: "1.0.0"}})
	if err != nil {
		t.Fatalf("marshal entries: %v", err)
	}
	body := `{"schema_version":"1","entries":` + string(entries)
	if seq != 0 {
		body += `,"sequence":` + strconv.FormatUint(seq, 10) + `,"published_at":"2026-01-01T00:00:00Z"`
	}
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte(body+"}")))
	return []byte(body + `,"signature":"` + sig + `"}`)
}

// swapFetcher serves whatever index the test last stored in it.
type swapFetcher struct{ index []byte }

func (f *swapFetcher) FetchIndex(context.Context) ([]byte, error) { return f.index, nil }
func (f *swapFetcher) FetchArtifact(context.Context, plugin.RegistryVersionEntry) ([]byte, error) {
	return nil, cascade.New(cascade.KindUnsupported, "not used by these tests")
}

// rollbackClient returns a cache-less client (every Fetch reaches the
// fetcher) whose baseline lives in a fresh CacheDir, plus that dir.
func rollbackClient(t *testing.T, f *swapFetcher) (*plugin.RegistryClient, string) {
	t.Helper()
	dir := t.TempDir()
	pub := rollbackKey().Public().(ed25519.PublicKey)
	cfg := plugin.RegistryConfig{RegistryURL: "https://registry.example.invalid", CacheDir: dir}
	return plugin.NewRegistryClient(cfg, f, plugin.Ed25519Verifier{PublicKey: pub}, nil, &fakeClock{now: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)}), dir
}

// seqFileSum returns the sha256 of index.seq, or "absent".
func seqFileSum(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "index.seq"))
	if os.IsNotExist(err) {
		return "absent"
	}
	if err != nil {
		t.Fatalf("read index.seq: %v", err)
	}
	sum := sha256.Sum256(data)
	return string(sum[:])
}

// mustFetch fetches and fails the test on error.
func mustFetch(t *testing.T, c *plugin.RegistryClient, step string) plugin.RegistryIndex {
	t.Helper()
	idx, err := c.Fetch(context.Background())
	if err != nil {
		t.Fatalf("%s: Fetch = %v, want accepted", step, err)
	}
	return idx
}

// assertRollbackRefusal fetches, wants the rollback refusal (Kind identity
// plus message), and wants index.seq byte-identical afterwards.
func assertRollbackRefusal(t *testing.T, c *plugin.RegistryClient, dir, want string) {
	t.Helper()
	before := seqFileSum(t, dir)
	_, err := c.Fetch(context.Background())
	var ce *cascade.Error
	if !errors.As(err, &ce) || ce.Kind != cascade.KindIntegrity || !strings.Contains(err.Error(), "registry index rollback") || !strings.Contains(err.Error(), want) {
		t.Fatalf("Fetch err = %v, want KindIntegrity \"registry index rollback\" naming %q", err, want)
	}
	if after := seqFileSum(t, dir); after != before {
		t.Fatal("index.seq changed after a refused index")
	}
}

func TestRegistryClient_FirstFetchHasNoBaseline(t *testing.T) {
	f := &swapFetcher{index: signedSeqIndex(t, rollbackKey(), 3, "v3")}
	c, dir := rollbackClient(t, f)
	if seqFileSum(t, dir) != "absent" {
		t.Fatal("fresh cache dir already holds index.seq")
	}
	if idx := mustFetch(t, c, "first fetch"); idx.Sequence != 3 || idx.PublishedAt != "2026-01-01T00:00:00Z" {
		t.Fatalf("first fetch = seq %d published %q, want 3 / 2026-01-01T00:00:00Z", idx.Sequence, idx.PublishedAt)
	}
	assertBaseline(t, dir, 3, f.index)
}

func TestRegistryClient_AcceptsSignedNewerIndex(t *testing.T) {
	f := &swapFetcher{index: signedSeqIndex(t, rollbackKey(), 1, "v1")}
	c, dir := rollbackClient(t, f)
	mustFetch(t, c, "seq 1")
	f.index = signedSeqIndex(t, rollbackKey(), 2, "v2")
	if idx := mustFetch(t, c, "seq 2"); idx.Entries[0].Name != "v2" {
		t.Fatalf("seq 2 served %q, want v2", idx.Entries[0].Name)
	}
	assertBaseline(t, dir, 2, f.index)
}

// TestRegistryClient_RejectsSignedOlderIndex also re-fetches the identical
// seq 2 index first: equal sequence with equal bytes is accepted, so a
// "<=" comparison fails here.
func TestRegistryClient_RejectsSignedOlderIndex(t *testing.T) {
	f := &swapFetcher{index: signedSeqIndex(t, rollbackKey(), 2, "v2")}
	c, dir := rollbackClient(t, f)
	mustFetch(t, c, "seq 2")
	mustFetch(t, c, "seq 2 again (same bytes)")
	f.index = signedSeqIndex(t, rollbackKey(), 1, "v1")
	assertRollbackRefusal(t, c, dir, "sequence 1 is older than the accepted sequence 2")
}

func TestRegistryClient_RejectsSameSequenceDifferentBytes(t *testing.T) {
	f := &swapFetcher{index: signedSeqIndex(t, rollbackKey(), 2, "v2")}
	c, dir := rollbackClient(t, f)
	mustFetch(t, c, "seq 2")
	f.index = signedSeqIndex(t, rollbackKey(), 2, "v2-forked")
	assertRollbackRefusal(t, c, dir, "sequence 2 was already accepted with different bytes")
}

func TestRegistryClient_RejectsSequenceZero(t *testing.T) {
	f := &swapFetcher{index: signedSeqIndex(t, rollbackKey(), 0, "legacy")}
	c, dir := rollbackClient(t, f)
	assertRollbackRefusal(t, c, dir, "sequence 0")
	f.index = signedSeqIndex(t, rollbackKey(), 1, "v1")
	mustFetch(t, c, "seq 1")
	f.index = signedSeqIndex(t, rollbackKey(), 0, "legacy")
	assertRollbackRefusal(t, c, dir, "sequence 0")
}

// TestUnverifiedIndexNeverMovesBaseline: a higher sequence under a bad
// signature is refused for its signature and leaves index.seq untouched.
// A client that records the sequence before verifying fails here.
func TestUnverifiedIndexNeverMovesBaseline(t *testing.T) {
	f := &swapFetcher{index: signedSeqIndex(t, rollbackKey(), 1, "v1")}
	c, dir := rollbackClient(t, f)
	mustFetch(t, c, "seq 1")
	before := seqFileSum(t, dir)
	_, otherPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	f.index = signedSeqIndex(t, otherPriv, 9, "forged")
	_, err = c.Fetch(context.Background())
	if !cascade.HasKind(err, cascade.KindIntegrity) || !strings.Contains(err.Error(), "signature does not verify") {
		t.Fatalf("Fetch(forged seq 9) err = %v, want the signature refusal", err)
	}
	if seqFileSum(t, dir) != before {
		t.Fatal("an unverified index moved the baseline")
	}
	f.index = signedSeqIndex(t, rollbackKey(), 2, "v2")
	mustFetch(t, c, "seq 2 after the forged seq 9")
}

// assertBaseline checks index.seq names exactly seq and sha256(raw) for the
// client's registry URL.
func assertBaseline(t *testing.T, dir string, seq uint64, raw []byte) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "index.seq"))
	if err != nil {
		t.Fatalf("read index.seq: %v", err)
	}
	var got map[string]struct {
		Sequence uint64 `json:"sequence"`
		SHA256   string `json:"sha256"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decode index.seq %s: %v", data, err)
	}
	sum := sha256.Sum256(raw)
	b := got["https://registry.example.invalid"]
	if len(got) != 1 || b.Sequence != seq || b.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("index.seq = %s, want only the registry URL at sequence %d with the sha256 of the accepted bytes", data, seq)
	}
}

// TestRegistryClient_CachedOlderIndexIsNotServed: a cached index older than
// the baseline is treated as a cache miss and the network index is served.
func TestRegistryClient_CachedOlderIndexIsNotServed(t *testing.T) {
	cacheDir, seqDir := t.TempDir(), t.TempDir()
	pub := rollbackKey().Public().(ed25519.PublicKey)
	cfg := plugin.RegistryConfig{RegistryURL: "r", CacheDir: seqDir, CacheTTL: time.Hour}
	clock := &fakeClock{now: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)}
	f := &swapFetcher{index: signedSeqIndex(t, rollbackKey(), 1, "v1")}
	mustFetch(t, plugin.NewRegistryClient(cfg, f, plugin.Ed25519Verifier{PublicKey: pub}, plugin.FileCache{Dir: cacheDir}, clock), "seq 1 cached")
	f.index = signedSeqIndex(t, rollbackKey(), 2, "v2")
	mustFetch(t, plugin.NewRegistryClient(cfg, f, plugin.Ed25519Verifier{PublicKey: pub}, nil, clock), "seq 2 uncached")
	idx := mustFetch(t, plugin.NewRegistryClient(cfg, f, plugin.Ed25519Verifier{PublicKey: pub}, plugin.FileCache{Dir: cacheDir}, clock), "cached seq 1 vs baseline 2")
	if idx.Sequence != 2 {
		t.Fatalf("served sequence %d from a stale cache, want the network's 2", idx.Sequence)
	}
}

// TestRegistryClient_CorruptBaselineRefuses: an unreadable index.seq fails
// closed and is left as found.
func TestRegistryClient_CorruptBaselineRefuses(t *testing.T) {
	c, dir := rollbackClient(t, &swapFetcher{index: signedSeqIndex(t, rollbackKey(), 1, "v1")})
	if err := os.WriteFile(filepath.Join(dir, "index.seq"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	assertRollbackRefusal(t, c, dir, "is corrupt")
}

// TestRegistryClient_WarnsOnStaleIndex: a verified index published more
// than 90 days before the clock is served with a warning naming its age.
func TestRegistryClient_WarnsOnStaleIndex(t *testing.T) {
	for _, tc := range []struct {
		now  time.Time
		want string
	}{
		{time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC), "registry index is 104 days old"},
		{time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC), ""},
	} {
		var logs strings.Builder
		cfg := plugin.RegistryConfig{CacheDir: t.TempDir(), Logger: slog.New(slog.NewTextHandler(&logs, nil))}
		f := &swapFetcher{index: signedSeqIndex(t, rollbackKey(), 1, "v1")}
		c := plugin.NewRegistryClient(cfg, f, plugin.Ed25519Verifier{PublicKey: rollbackKey().Public().(ed25519.PublicKey)}, nil, &fakeClock{now: tc.now})
		mustFetch(t, c, tc.now.String())
		if got := logs.String(); (tc.want == "") != (got == "") || !strings.Contains(got, tc.want) {
			t.Errorf("at %s logs = %q, want %q", tc.now, got, tc.want)
		}
	}
}

// TestRegistryClient_CacheHitSeedsMissingBaseline: a verified cached index
// re-seeds a deleted index.seq, and two registry URLs keep separate entries.
func TestRegistryClient_CacheHitSeedsMissingBaseline(t *testing.T) {
	cacheDir, seqDir := t.TempDir(), t.TempDir()
	pub := rollbackKey().Public().(ed25519.PublicKey)
	clock := &fakeClock{now: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)}
	f := &swapFetcher{index: signedSeqIndex(t, rollbackKey(), 4, "v4")}
	cfg := plugin.RegistryConfig{RegistryURL: "https://registry.example.invalid", CacheDir: seqDir, CacheTTL: time.Hour}
	c := plugin.NewRegistryClient(cfg, f, plugin.Ed25519Verifier{PublicKey: pub}, plugin.FileCache{Dir: cacheDir}, clock)
	mustFetch(t, c, "seq 4 fetched and cached")
	if err := os.Remove(filepath.Join(seqDir, "index.seq")); err != nil {
		t.Fatal(err)
	}
	f.index = nil // a network fetch would now fail verification
	mustFetch(t, c, "seq 4 from cache")
	assertBaseline(t, seqDir, 4, signedSeqIndex(t, rollbackKey(), 4, "v4"))
	other := plugin.RegistryConfig{RegistryURL: "https://other.example.invalid", CacheDir: seqDir}
	f.index = signedSeqIndex(t, rollbackKey(), 1, "other-v1")
	mustFetch(t, plugin.NewRegistryClient(other, f, plugin.Ed25519Verifier{PublicKey: pub}, nil, clock), "other URL seq 1")
	data, err := os.ReadFile(filepath.Join(seqDir, "index.seq"))
	if err != nil || !strings.Contains(string(data), `"https://registry.example.invalid":{"sequence":4`) || !strings.Contains(string(data), `"https://other.example.invalid":{"sequence":1`) {
		t.Fatalf("index.seq = %s (err %v), want both URLs with their own sequences", data, err)
	}
	if info, err := os.Stat(filepath.Join(seqDir, "index.seq")); err == nil && runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("index.seq mode = %v, want 0600", info.Mode().Perm())
	}
}
