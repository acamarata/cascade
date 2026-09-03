package rpc

// Purpose: FuzzParseRequest — the fuzz target this ticket's contract
//   requires (06-FORGE-SPEC §5 rule 7: "any ticket adding a parser/decoder
//   MUST include a FuzzXxx target"). Parse decodes untrusted bytes straight
//   from an HTTP body, so it must never panic no matter how malformed the
//   input.
// Constraints: seed corpus lives at
//   internal/testdata/fuzz/FuzzParseRequest/ (never repo root), with a
//   provenance README this test asserts exists.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// fuzzSeedDir is relative to this package's own directory, matching how Go
// fuzzing resolves testdata/fuzz/<FuzzName>/ seed corpus files.
const fuzzSeedDir = "../testdata/fuzz/FuzzParseRequest"

// TestFuzzParseRequestSeedProvenanceExists asserts the corpus provenance
// README this ticket's contract requires actually exists — part of the
// FuzzParseRequest AC ("seed corpus ... with provenance README").
func TestFuzzParseRequestSeedProvenanceExists(t *testing.T) {
	path := filepath.Join(fuzzSeedDir, "README.md")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("provenance README missing at %s: %v", path, err)
	}
	if info.IsDir() {
		t.Fatalf("%s is a directory, want a file", path)
	}
}

// loadFuzzSeedFiles reads every *.json file in fuzzSeedDir and returns its
// raw contents as seed strings. tb is either *testing.T (the provenance
// existence test's sibling assertions) or *testing.F (FuzzParseRequest's
// seeding); both satisfy the small subset of *testing.common used here.
func loadFuzzSeedFiles(f *testing.F) []string {
	f.Helper()
	entries, err := os.ReadDir(fuzzSeedDir)
	if err != nil {
		f.Fatalf("reading fuzz seed dir %s: %v", fuzzSeedDir, err)
	}
	var seeds []string
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		data, readErr := os.ReadFile(filepath.Join(fuzzSeedDir, e.Name()))
		if readErr != nil {
			f.Fatalf("reading seed file %s: %v", e.Name(), readErr)
		}
		seeds = append(seeds, string(data))
	}
	if len(seeds) == 0 {
		f.Fatalf("no *.json seed files found in %s", fuzzSeedDir)
	}
	return seeds
}

func FuzzParseRequest(f *testing.F) {
	for _, s := range loadFuzzSeedFiles(f) {
		f.Add(s)
	}
	// A few additional literal edge cases a *.json seed file cannot itself
	// express cleanly (an entirely empty string vs. an empty file, and a
	// field-type-mismatched value).
	for _, s := range []string{"", `{}`, `{"jsonrpc":"2.0","method":123}`} {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, in string) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("Parse panicked on input %q: %v", in, r)
			}
		}()
		req, errObj := Parse([]byte(in))
		if errObj != nil {
			return
		}
		// A successfully parsed request must always re-marshal its ID and
		// Params without error — Parse must never hand back a Request
		// whose raw fields are not valid JSON fragments.
		if len(req.ID) > 0 {
			var v any
			if err := json.Unmarshal(req.ID, &v); err != nil {
				t.Fatalf("Parse accepted invalid id bytes %q: %v", req.ID, err)
			}
		}
		if len(req.Params) > 0 {
			var v any
			if err := json.Unmarshal(req.Params, &v); err != nil {
				t.Fatalf("Parse accepted invalid params bytes %q: %v", req.Params, err)
			}
		}
	})
}
