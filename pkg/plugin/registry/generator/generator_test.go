// Purpose: golden, idempotency, error-path, and real-counterpart
//
//	integration tests for the registry index generator.
//
// Inputs: the committed fixture pairs under testdata/fixtures/ plus a
//
//	fixed (non-production) Ed25519 seed for deterministic signatures.
//
// Outputs: none — pass/fail only.
// Constraints: zero network calls; all mutable IO uses t.TempDir(). The
//
//	"integration test calling the real T1 client" AC names a constructor,
//	NewHTTPRegistryClient, that does not exist in the tree — the real
//	client is plugin.NewRegistryClient with injected seams (registry_
//	client.go). This test feeds the generator's bytes through the real
//	Ed25519Verifier via an in-memory RegistryFetcher stub, which proves
//	the same claim (the real client accepts this generator's output)
//	without needing HTTP transport pkg/ cannot import anyway.
//
// SPORT: pkg/plugin/registry/generator tests (ADD) — P1-E24-W5-S50-T5.
package generator_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
	"github.com/acamarata/cascade/pkg/plugin/registry/generator"
)

// fixedSeed is a 32-byte Ed25519 seed used only to make this suite's
// signatures reproducible across runs; it signs no real registry index
// anywhere and is not the production registry signing key.
var fixedSeed = []byte{
	1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16,
	17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32,
}

type testVault struct {
	key []byte
	err error
}

func (v testVault) Get(_ context.Context, _ string) ([]byte, error) { return v.key, v.err }

func fixedVault() testVault {
	return testVault{key: ed25519.NewKeyFromSeed(fixedSeed)}
}

func fixedPublicKey() ed25519.PublicKey {
	return ed25519.NewKeyFromSeed(fixedSeed).Public().(ed25519.PublicKey)
}

const goldenPath = "../../../../internal/testdata/goldens/registry-generator/index.json"

// fixturesConfig is the golden's generator input: sequence 1, published
// 2026-01-01T00:00:00Z (sub-second input proves the truncation).
func fixturesConfig() generator.GeneratorConfig {
	return generator.GeneratorConfig{ArtifactsDir: "testdata/fixtures", BaseURL: "https://cdn.example.com/plugins/",
		Sequence: 1, PublishedAt: time.Date(2026, 1, 1, 0, 0, 0, 999, time.UTC)}
}

func TestGenerateIndexGoldenDeterministicAndIdempotent(t *testing.T) {
	cfg := fixturesConfig()

	idx1, err := generator.GenerateIndex(cfg)
	if err != nil {
		t.Fatalf("GenerateIndex #1: %v", err)
	}
	idx2, err := generator.GenerateIndex(cfg)
	if err != nil {
		t.Fatalf("GenerateIndex #2: %v", err)
	}
	if !reflect.DeepEqual(idx1, idx2) {
		t.Fatalf("GenerateIndex is not idempotent:\n%#v\n%#v", idx1, idx2)
	}

	if err := generator.SignIndex(idx1, "test-key-ref", fixedVault()); err != nil {
		t.Fatalf("SignIndex: %v", err)
	}
	outDir := t.TempDir()
	if err := generator.WriteIndex(idx1, outDir); err != nil {
		t.Fatalf("WriteIndex: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(outDir, "index.json"))
	if err != nil {
		t.Fatalf("read written index.json: %v", err)
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden %s: %v", goldenPath, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("index.json does not match golden:\ngot:  %s\nwant: %s", got, want)
	}
}

func TestGenerateIndexChecksumsMatchSourceFiles(t *testing.T) {
	cfg := fixturesConfig()
	idx, err := generator.GenerateIndex(cfg)
	if err != nil {
		t.Fatalf("GenerateIndex: %v", err)
	}
	for _, e := range idx.Entries {
		for _, v := range e.Versions {
			base := e.ID + "-" + v.Version + ".plugin"
			data, err := os.ReadFile(filepath.Join("testdata", "fixtures", base))
			if err != nil {
				t.Fatalf("read source artifact %s: %v", base, err)
			}
			sum := sha256.Sum256(data)
			want := hex.EncodeToString(sum[:])
			if v.Checksum != want {
				t.Errorf("entry %s@%s checksum = %s, want %s", e.ID, v.Version, v.Checksum, want)
			}
		}
	}
}

func TestGenerateIndexErrMissingManifest(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "orphan-1.0.0.plugin"), []byte("bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := generator.GenerateIndex(generator.GeneratorConfig{ArtifactsDir: dir, Sequence: 1, PublishedAt: time.Unix(1, 0)})
	if !errors.Is(err, generator.ErrMissingManifest) {
		t.Fatalf("err = %v, want errors.Is ErrMissingManifest", err)
	}
	if got := err.Error(); !bytes.Contains([]byte(got), []byte("no co-located manifest")) {
		t.Errorf("error message %q does not name the missing-manifest case", got)
	}
}

func TestGenerateIndexErrInvalidManifest(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bad-1.0.0.plugin"), []byte("bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	// schema field wrong: fails manifest v2 validation (rule R1).
	bad := "id = \"bad\"\nname = \"Bad\"\nschema = \"not-a-real-schema\"\nversion = \"1.0.0\"\nhost_version = \">=1.0.0\"\nruntime = \"wasm\"\n"
	if err := os.WriteFile(filepath.Join(dir, "bad-1.0.0.manifest.toml"), []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := generator.GenerateIndex(generator.GeneratorConfig{ArtifactsDir: dir, Sequence: 1, PublishedAt: time.Unix(1, 0)})
	if !errors.Is(err, generator.ErrInvalidManifest) {
		t.Fatalf("err = %v, want errors.Is ErrInvalidManifest", err)
	}
	if got := err.Error(); !bytes.Contains([]byte(got), []byte("failed v2 schema validation")) {
		t.Errorf("error message %q does not name the invalid-manifest case", got)
	}
}

func TestSignIndexPropagatesVaultError(t *testing.T) {
	idx := &plugin.RegistryIndex{SchemaVersion: plugin.SchemaVersionCurrent, Sequence: 1, PublishedAt: "2026-01-01T00:00:00Z"}
	wantErr := errors.New("vault unreachable")
	err := generator.SignIndex(idx, "any-ref", testVault{err: wantErr})
	if err == nil {
		t.Fatal("SignIndex returned nil error on vault failure")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("err = %v, want it to wrap %v", err, wantErr)
	}
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Errorf("err kind = %v, want KindUnavailable", err)
	}
}

// memFetcher and fixedClock let the integration test drive the real
// plugin.RegistryClient without HTTP or a wall-clock read.
type memFetcher struct{ data []byte }

func (f memFetcher) FetchIndex(_ context.Context) ([]byte, error) { return f.data, nil }
func (f memFetcher) FetchArtifact(_ context.Context, _ plugin.RegistryVersionEntry) ([]byte, error) {
	return nil, nil
}

type fixedClock struct{ t time.Time }

func (c fixedClock) Now() time.Time { return c.t }

func TestIntegrationRealClientAcceptsGeneratedIndex(t *testing.T) {
	cfg := fixturesConfig()
	idx, err := generator.GenerateIndex(cfg)
	if err != nil {
		t.Fatalf("GenerateIndex: %v", err)
	}
	if err := generator.SignIndex(idx, "test-key-ref", fixedVault()); err != nil {
		t.Fatalf("SignIndex: %v", err)
	}
	outDir := t.TempDir()
	if err := generator.WriteIndex(idx, outDir); err != nil {
		t.Fatalf("WriteIndex: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(outDir, "index.json"))
	if err != nil {
		t.Fatalf("read index.json: %v", err)
	}

	verifier := plugin.Ed25519Verifier{PublicKey: fixedPublicKey()}
	client := plugin.NewRegistryClient(
		plugin.RegistryConfig{PublicKey: fixedPublicKey()},
		memFetcher{data: data},
		verifier,
		nil,
		fixedClock{t: time.Unix(0, 0)},
	)
	got, err := client.Fetch(context.Background())
	if err != nil {
		t.Fatalf("real client Fetch over generated index: %v", err)
	}
	if len(got.Entries) != len(idx.Entries) {
		t.Fatalf("client returned %d entries, generator produced %d", len(got.Entries), len(idx.Entries))
	}
}

// TestGeneratorRequiresSequence: a missing or zero sequence (or an unset
// published_at) refuses before anything is signed; SignIndex never even
// asks the vault for the key.
func TestGeneratorRequiresSequence(t *testing.T) {
	for name, cfg := range map[string]generator.GeneratorConfig{
		"zero sequence":     {ArtifactsDir: "testdata/fixtures", PublishedAt: time.Unix(1, 0)},
		"zero published_at": {ArtifactsDir: "testdata/fixtures", Sequence: 1},
	} {
		idx, err := generator.GenerateIndex(cfg)
		if idx != nil || !errors.Is(err, generator.ErrMissingSequence) || !strings.Contains(err.Error(), "sequence (>= 1)") {
			t.Errorf("%s: GenerateIndex = %v, %v; want nil and ErrMissingSequence", name, idx, err)
		}
	}
	for name, idx := range map[string]*plugin.RegistryIndex{
		"zero sequence":      {SchemaVersion: "1", PublishedAt: "2026-01-01T00:00:00Z"},
		"empty published_at": {SchemaVersion: "1", Sequence: 1},
	} {
		vault := &countingVault{testVault: fixedVault()}
		err := generator.SignIndex(idx, "test-key-ref", vault)
		if !errors.Is(err, generator.ErrMissingSequence) || idx.Signature != "" || vault.calls != 0 {
			t.Errorf("%s: SignIndex err=%v signature=%q vault calls=%d; want ErrMissingSequence, unsigned, 0 calls", name, err, idx.Signature, vault.calls)
		}
	}
}

// countingVault records how often the signing key was requested.
type countingVault struct {
	testVault
	calls int
}

func (v *countingVault) Get(ctx context.Context, ref string) ([]byte, error) {
	v.calls++
	return v.testVault.Get(ctx, ref)
}
