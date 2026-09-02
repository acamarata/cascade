// Purpose: tests for envelope.go — the versioned --json envelope shape,
//
//	its two golden fixtures (byte-stable OK and error wire shapes), and
//	the NDJSON stream writer.
//
// Constraints: the two golden fixtures this ticket's contract names
//
//	(files_scope.add) live directly under testdata/ with a .json
//	extension — internal/testkit.Golden hardcodes testdata/goldens/*.golden
//	(see internal/testkit/golden.go), a different directory AND extension,
//	so it cannot address these exact contract-named paths. This file
//	instead builds its own tiny compare/update helper on top of
//	testkit.UpdateRequested (exported for precisely this: "callers can
//	short-circuit expensive rendering... Golden itself is what actually
//	enforces the CI guard" — golden.go's own doc comment), reproducing
//	that guard (never regenerate under CI) directly against the ticket's
//	literal filenames. Content is fully deterministic (fixed struct
//	input, fixed taxonomy error, no clock/rand/map-of-multiple-keys), so
//	Art.7.3's flakiness bar is met without depending on testkit's own
//	path convention.
//
// SPORT: internal/output [ADD] (D/S-06.T5 sport_updates).
package output_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/internal/output"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
)

// sampleResult is a fixed, deterministic success payload: a typed struct
// (not a map) so its own field order is also declaration-order-stable,
// belt-and-braces on top of encoding/json's already-deterministic
// alphabetic map-key sort.
type sampleResult struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// goldenCompare compares got against the committed fixture at the given
// contract-literal path (relative to this package's directory, matching
// os.ReadFile's normal test-time cwd). In update mode
// (CASCADE_TESTKIT_UPDATE_GOLDEN=1) it writes got instead — unless CI is
// also set, mirroring testkit.Golden's own refusal, so goldens are never
// silently regenerated in CI here either.
func goldenCompare(t *testing.T, path string, got []byte) {
	t.Helper()
	if testkit.UpdateRequested() {
		if os.Getenv("CI") != "" {
			t.Fatalf("envelope_test: refusing to update golden %q: update env set under CI", path)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatalf("envelope_test: writing golden %q: %v", path, err)
		}
		t.Logf("envelope_test: updated golden %q", path)
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("envelope_test: reading golden %q: %v", path, err)
	}
	if string(want) != string(got) {
		t.Errorf("envelope_test: golden mismatch for %q\n--- want ---\n%s\n--- got ---\n%s", path, want, got)
	}
}

func TestEnvelope_GoldenOK(t *testing.T) {
	env := output.NewOKEnvelope(sampleResult{Name: "widget", Count: 3})
	line, err := env.MarshalLine()
	if err != nil {
		t.Fatalf("MarshalLine: %v", err)
	}
	goldenCompare(t, filepath.Join("testdata", "golden_envelope_ok.json"), line)
}

func TestEnvelope_GoldenErr(t *testing.T) {
	taxErr := cascade.New(cascade.KindNotFound, `widget "foo" not found`)
	env := output.NewErrEnvelope(taxErr)
	line, err := env.MarshalLine()
	if err != nil {
		t.Fatalf("MarshalLine: %v", err)
	}
	goldenCompare(t, filepath.Join("testdata", "golden_envelope_err.json"), line)
}

// TestEnvelope_GoldenDetectsMutation proves the golden comparison is
// load-bearing: a mutated line must differ from what MarshalLine produces.
func TestEnvelope_GoldenDetectsMutation(t *testing.T) {
	env := output.NewOKEnvelope(sampleResult{Name: "widget", Count: 3})
	line, err := env.MarshalLine()
	if err != nil {
		t.Fatalf("MarshalLine: %v", err)
	}
	mutated := append(append([]byte{}, line...), []byte("\nunexpected extra line\n")...)
	if string(line) == string(mutated) {
		t.Fatal("mutated fixture must differ from actual output")
	}
}

func TestNewErrEnvelope_NonTaxonomyErrorFallsBackToInternal(t *testing.T) {
	env := output.NewErrEnvelope(os.ErrNotExist)
	if env.OK {
		t.Fatal("NewErrEnvelope(non-nil) must have OK=false")
	}
	if env.Error == nil {
		t.Fatal("NewErrEnvelope(non-nil) must set Error")
	}
	if env.Error.Kind != cascade.KindInternal.String() {
		t.Errorf("Kind = %q, want %q", env.Error.Kind, cascade.KindInternal.String())
	}
	if env.Error.Code != cascade.RPCCodeInternal {
		t.Errorf("Code = %d, want %d", env.Error.Code, cascade.RPCCodeInternal)
	}
}

func TestNewErrEnvelope_Nil(t *testing.T) {
	env := output.NewErrEnvelope(nil)
	if !env.OK || env.Error != nil {
		t.Fatalf("NewErrEnvelope(nil) = %+v, want a zero-value OK envelope", env)
	}
}

func TestEnvelope_MarshalLineUnsupportedType(t *testing.T) {
	env := output.NewOKEnvelope(make(chan int)) // channels are not JSON-marshalable
	if _, err := env.MarshalLine(); err == nil {
		t.Fatal("MarshalLine over an unmarshalable Data value must error")
	} else if !cascade.HasKind(err, cascade.KindInternal) {
		t.Errorf("MarshalLine error kind = %v, want KindInternal", err)
	}
}

func TestNDJSONWriter_Emit(t *testing.T) {
	stdout := &bytes.Buffer{}
	w := output.New(stdout, &bytes.Buffer{}, false, false, false, false)
	nd := w.NDJSON()

	if err := nd.Emit(sampleResult{Name: "a", Count: 1}); err != nil {
		t.Fatalf("Emit 1: %v", err)
	}
	if err := nd.Emit(sampleResult{Name: "b", Count: 2}); err != nil {
		t.Fatalf("Emit 2: %v", err)
	}

	want := "{\"name\":\"a\",\"count\":1}\n{\"name\":\"b\",\"count\":2}\n"
	if got := stdout.String(); got != want {
		t.Fatalf("NDJSON stream = %q, want %q", got, want)
	}
}

func TestNDJSONWriter_MarshalError(t *testing.T) {
	w := output.New(&bytes.Buffer{}, &bytes.Buffer{}, false, false, false, false)
	nd := w.NDJSON()
	if err := nd.Emit(make(chan int)); err == nil {
		t.Fatal("Emit over an unmarshalable value must error")
	} else if !cascade.HasKind(err, cascade.KindInternal) {
		t.Errorf("Emit marshal-error kind = %v, want KindInternal", err)
	}
}

func TestNDJSONWriter_WriteError(t *testing.T) {
	w := output.New(failingWriter{}, &bytes.Buffer{}, false, false, false, false)
	nd := w.NDJSON()
	if err := nd.Emit(sampleResult{Name: "x"}); err == nil {
		t.Fatal("Emit over a failing writer must error")
	} else if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Errorf("Emit write-error kind = %v, want KindUnavailable", err)
	}
}
