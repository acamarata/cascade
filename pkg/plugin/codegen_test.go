// Purpose: unit tests for codegen.go — the golden manifest round-trips
//
//	through Validate with zero errors, EncodeManifest/EncodeManifestTOML
//	are idempotent, and the checked-in testdata/codegen-example.toml
//	fixture stays in sync with GenerateExampleManifest. TestGenerateManifestGolden
//	is the regeneration path codegen.go's go:generate directive invokes;
//	it is skipped in a normal `go test` run and only writes when
//	CASCADE_GENERATE=1 is set, so this file has no side effects outside
//	`go generate`.
//
// Constraints: Art.1 — every double here is a genuine failing io.Writer,
//
//	not a placeholder; the golden-drift test is not a "no panic" assertion.
//
// SPORT: pkg/plugin manifest-codegen tests (ADD) — P1-E15-W4-S33-T1.
package plugin_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
)

const codegenGoldenPath = "testdata/codegen-example.toml"

// failingWriter always returns an error from Write, exercising
// EncodeManifest's error path without weakening EncodeManifest itself.
type failingWriter struct{}

func (failingWriter) Write(_ []byte) (int, error) {
	return 0, errors.New("simulated write failure")
}

func TestGenerateExampleManifestValidates(t *testing.T) {
	m := plugin.GenerateExampleManifest()
	if errs := plugin.Validate(m); len(errs) != 0 {
		t.Fatalf("GenerateExampleManifest() failed Validate: %v", errs)
	}
}

func TestEncodeManifestTOML_RoundTrip(t *testing.T) {
	m := plugin.GenerateExampleManifest()
	data, err := plugin.EncodeManifestTOML(m)
	if err != nil {
		t.Fatalf("EncodeManifestTOML: %v", err)
	}

	got, err := plugin.ParseManifest(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("ParseManifest(encoded output): %v", err)
	}
	if got.ID != m.ID || got.Version != m.Version || len(got.Provides.Tools) != len(m.Provides.Tools) {
		t.Fatalf("round-trip mismatch: got %+v, want %+v", got, m)
	}
}

func TestEncodeManifestTOML_Idempotent(t *testing.T) {
	m := plugin.GenerateExampleManifest()
	first, err := plugin.EncodeManifestTOML(m)
	if err != nil {
		t.Fatalf("EncodeManifestTOML (first): %v", err)
	}
	second, err := plugin.EncodeManifestTOML(m)
	if err != nil {
		t.Fatalf("EncodeManifestTOML (second): %v", err)
	}
	if string(first) != string(second) {
		t.Fatalf("EncodeManifestTOML is not idempotent:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
}

func TestEncodeManifest_WriteError(t *testing.T) {
	err := plugin.EncodeManifest(failingWriter{}, plugin.GenerateExampleManifest())
	if err == nil {
		t.Fatal("EncodeManifest with a failing writer: want error, got nil")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInternal {
		t.Fatalf("EncodeManifest write-error kind = %v, ok=%v; want KindInternal", kind, ok)
	}
}

// TestCodegenGoldenUpToDate is the real idempotency/drift check: it runs
// in every normal `go test` invocation (unlike TestGenerateManifestGolden
// below) and fails if GenerateExampleManifest's output has drifted from
// the checked-in fixture, which is what `go generate ./pkg/plugin/... &&
// git diff --exit-code` (this ticket's check) ultimately verifies.
func TestCodegenGoldenUpToDate(t *testing.T) {
	want, err := os.ReadFile(codegenGoldenPath)
	if err != nil {
		t.Fatalf("reading %s: %v (run `go generate ./pkg/plugin/...` first)", codegenGoldenPath, err)
	}
	got, err := plugin.EncodeManifestTOML(plugin.GenerateExampleManifest())
	if err != nil {
		t.Fatalf("EncodeManifestTOML: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("%s is stale; run `go generate ./pkg/plugin/...`\ngot:\n%s\nwant:\n%s", codegenGoldenPath, got, want)
	}
}

// TestGenerateManifestGolden is the regeneration path codegen.go's
// //go:generate directive runs. It writes only when CASCADE_GENERATE=1 is
// set (set by that directive), so a plain `go test ./pkg/plugin/...` never
// touches the filesystem here.
func TestGenerateManifestGolden(t *testing.T) {
	if os.Getenv("CASCADE_GENERATE") == "" {
		t.Skip("run via `go generate ./pkg/plugin/...`")
	}
	data, err := plugin.EncodeManifestTOML(plugin.GenerateExampleManifest())
	if err != nil {
		t.Fatalf("EncodeManifestTOML: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(codegenGoldenPath), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(codegenGoldenPath, data, 0o644); err != nil {
		t.Fatalf("WriteFile %s: %v", codegenGoldenPath, err)
	}
}
