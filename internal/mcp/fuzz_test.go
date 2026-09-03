package mcp

// Purpose: FuzzMCPFrame — the fuzz target 06-FORGE-SPEC §5 rule 7 requires
//   for ParseFrame, the decoder that reads one line of untrusted bytes off
//   either MCP transport (stdio directly; socket indirectly, via a
//   JSON-RPC params field that is itself untrusted client input).
//   ParseFrame must never panic, however malformed its input.
// Constraints: seed corpus at the canonical rule-5.7 path
//   internal/testdata/fuzz/FuzzMCPFrame/ (never under internal/mcp/testdata,
//   which holds only the wire-golden provenance README).

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

const fuzzSeedDir = "../testdata/fuzz/FuzzMCPFrame"

// TestFuzzMCPFrameSeedProvenanceExists asserts the corpus provenance
// README this ticket's contract requires actually exists.
func TestFuzzMCPFrameSeedProvenanceExists(t *testing.T) {
	path := filepath.Join(fuzzSeedDir, "README.md")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("provenance README missing at %s: %v", path, err)
	}
	if info.IsDir() {
		t.Fatalf("%s is a directory, want a file", path)
	}
}

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

// FuzzMCPFrame fuzzes ParseFrame directly. Its only property: never panic,
// and any successfully parsed Frame's raw sub-fields (ID, Params) must
// themselves re-marshal as valid JSON — ParseFrame must never hand back a
// Frame carrying corrupt raw bytes.
func FuzzMCPFrame(f *testing.F) {
	for _, s := range loadFuzzSeedFiles(f) {
		f.Add(s)
	}
	for _, s := range []string{
		"", "{}", "null", `{"jsonrpc":"2.0"`, // truncated
		`{"jsonrpc":"2.0","method":123}`, // wrong type
		`{"jsonrpc":"2.0","method":"tools/list","mcp_method":"tools/list","mcp_name":"c","id":1}`,
	} {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, in string) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("ParseFrame panicked on input %q: %v", in, r)
			}
		}()
		frame, errObj := ParseFrame([]byte(in))
		if errObj != nil {
			return
		}
		if len(frame.ID) > 0 {
			var v any
			if err := json.Unmarshal(frame.ID, &v); err != nil {
				t.Fatalf("ParseFrame accepted invalid id bytes %q: %v", frame.ID, err)
			}
		}
		if len(frame.Params) > 0 {
			var v any
			if err := json.Unmarshal(frame.Params, &v); err != nil {
				t.Fatalf("ParseFrame accepted invalid params bytes %q: %v", frame.Params, err)
			}
		}
	})
}
