// Purpose: FuzzStreamResult, the required fuzz target for streamResult's
//
//	SSE data:-line parse loop (06-FORGE-SPEC.md §5 rule 7: externally-
//	defined framing must be fuzzed). Seed corpus at
//	testdata/fuzz/FuzzStreamResult/ (valid, truncated, and malformed SSE
//	frames).
//
// SPORT: cmd/cascade/run (ADD, P1-E11-W3-S23-T1).
package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// FuzzStreamResult proves streamResult never panics on arbitrary bytes,
// however malformed - a truncated body, a body with no trailing newline,
// or garbage that never matches "data:" at all.
func FuzzStreamResult(f *testing.F) {
	seeds := []string{
		"id: 1\ndata: {\"kind\":\"delta\"}\n\nid: 2\ndata: {\"kind\":\"done\"}\n\n",
		"data: only-one-line",
		": keep-alive\n",
		"",
		"data:",
		"data: \n",
		"\x00\x01\xff garbage not sse at all",
		"data: line-with-no-trailing-newline",
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		var out bytes.Buffer
		// streamResult must never panic; a returned error (e.g. an
		// already-cancelled ctx, never true here) is acceptable, a panic
		// is not.
		_ = streamResult(context.Background(), &out, bytes.NewReader(data))
		// Every line the loop actually wrote must have come from a
		// "data:"-prefixed input line - the parser never invents content.
		for _, written := range strings.Split(strings.TrimRight(out.String(), "\n"), "\n") {
			if written == "" {
				continue
			}
			if !bytes.Contains(data, []byte(written)) {
				t.Fatalf("streamResult wrote %q, which does not appear anywhere in its input %q", written, data)
			}
		}
	})
}
