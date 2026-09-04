package client

// Purpose: this SDK's two required fuzz targets (06-FORGE-SPEC §5 rule 7,
//   hard requirement 2): FuzzRPCResponseDecode over decodeEnvelope
//   (decode.go — the JSON-RPC response decoder) and FuzzSSEEventParse over
//   sseAccumulator.feed (stream.go — the SSE field-line parser). Both
//   decode bytes from the far end of the daemon's unix socket, i.e.
//   untrusted input by this repo's own definition, and neither may ever
//   panic, block, or allocate unboundedly, however truncated, oversized,
//   garbage, or mismatched-id the input is.
// Constraints: seed corpora at
//   internal/testdata/fuzz/FuzzRPCResponseDecode/ and
//   internal/testdata/fuzz/FuzzSSEEventParse/, each with a provenance
//   README this file's provenance-existence tests assert exist.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// rpcFuzzSeedDir mirrors internal/rpc/fuzz_test.go's fuzzSeedDir
// convention for this package's own corpus.
const rpcFuzzSeedDir = "../testdata/fuzz/FuzzRPCResponseDecode"

// sseFuzzSeedDir mirrors the same convention for the SSE corpus.
const sseFuzzSeedDir = "../testdata/fuzz/FuzzSSEEventParse"

func TestFuzzRPCResponseDecodeSeedProvenanceExists(t *testing.T) {
	assertProvenanceReadme(t, rpcFuzzSeedDir)
}

func TestFuzzSSEEventParseSeedProvenanceExists(t *testing.T) {
	assertProvenanceReadme(t, sseFuzzSeedDir)
}

func assertProvenanceReadme(t *testing.T, dir string) {
	t.Helper()
	path := filepath.Join(dir, "README.md")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("provenance README missing at %s: %v", path, err)
	}
	if info.IsDir() {
		t.Fatalf("%s is a directory, want a file", path)
	}
}

// loadSeedFiles reads every file with ext in dir, mirroring
// internal/rpc/fuzz_test.go's loadFuzzSeedFiles.
func loadSeedFiles(f *testing.F, dir, ext string) []string {
	f.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		f.Fatalf("reading fuzz seed dir %s: %v", dir, err)
	}
	var seeds []string
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ext {
			continue
		}
		data, readErr := os.ReadFile(filepath.Join(dir, e.Name()))
		if readErr != nil {
			f.Fatalf("reading seed file %s: %v", e.Name(), readErr)
		}
		seeds = append(seeds, string(data))
	}
	if len(seeds) == 0 {
		f.Fatalf("no *%s seed files found in %s", ext, dir)
	}
	return seeds
}

// FuzzRPCResponseDecode proves decodeEnvelope never panics on any input,
// however malformed, and that a successfully decoded envelope's Result
// bytes (if present) always re-parse as JSON — decodeEnvelope must never
// hand back a wireEnvelope whose raw Result field is not a valid JSON
// fragment.
func FuzzRPCResponseDecode(f *testing.F) {
	for _, s := range loadSeedFiles(f, rpcFuzzSeedDir, ".json") {
		f.Add([]byte(s))
	}
	for _, s := range [][]byte{
		nil, {}, []byte("{"), []byte(strings.Repeat("[", 10000)),
		[]byte(`{"jsonrpc":"2.0","id":"x","result":123}`),
		[]byte(`{"jsonrpc":"2.0","id":"mismatched-id","error":{"code":-32601,"message":"m"}}`),
	} {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, in []byte) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("decodeEnvelope panicked on input %q: %v", in, r)
			}
		}()
		env, err := decodeEnvelope(in)
		if err != nil {
			return
		}
		if len(env.Result) == 0 {
			return
		}
		if !json.Valid(env.Result) {
			t.Fatalf("decodeEnvelope accepted invalid result bytes %q", env.Result)
		}
	})
}

// FuzzSSEEventParse proves sseAccumulator.feed never panics on any input
// line, and that accumulator state never grows without bound relative to
// what feed itself was given (no internal buffering beyond the lines fed
// to it).
func FuzzSSEEventParse(f *testing.F) {
	for _, s := range loadSeedFiles(f, sseFuzzSeedDir, ".txt") {
		f.Add(s)
	}
	for _, s := range []string{
		"", ":", "id", "id:", "data:", "id:1\ndata:x\n\n",
		strings.Repeat("data:x", 10000),
	} {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, in string) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("sseAccumulator.feed panicked on input %q: %v", in, r)
			}
		}()
		var acc sseAccumulator
		total := 0
		for _, line := range strings.Split(in, "\n") {
			ev, complete := acc.feed(line)
			if complete {
				total += len(ev.ID) + len(ev.Data)
			}
		}
		if total > len(in)+1 {
			t.Fatalf("feed emitted more data (%d bytes) than it was ever fed (%d bytes) for input %q", total, len(in), in)
		}
	})
}
