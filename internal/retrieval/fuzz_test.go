// Purpose: FuzzChunk, the fuzz target 06-FORGE-SPEC.md §5 rule 7 requires
// for this ticket (md/code chunkers are parsers over untrusted input:
// arbitrary files an ingest walk hands them, never validated upstream).
// Its only property: chunking never panics and never returns a chunk
// whose byte range falls outside the input, for any byte sequence, run
// against every chunker/language this package ships.
//
// Constraints: seed corpus at the canonical rule-5.7 shared path
// internal/testdata/fuzz/FuzzChunk/ (never under
// internal/retrieval/testdata, which holds only golden-fixture
// provenance) — see that directory's own README.md. This ticket's own
// contract names internal/retrieval/testdata/fuzz/FuzzChunk/ instead;
// the tree's established convention (every other FuzzXxx target in this
// module: internal/mcp, internal/events, internal/rpc,
// internal/events/scheduler) puts every corpus under the shared location,
// so this target follows the tree.
//
// SPORT: internal.retrieval.FuzzChunk/ADDED (P1-E06-W2-S10-T1).
package retrieval

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fuzzChunkSeedDir is the shared fuzz-corpus home for FuzzChunk, relative
// to this package's directory.
const fuzzChunkSeedDir = "../testdata/fuzz/FuzzChunk"

// fuzzLangs is the set of chunker configurations FuzzChunk drives every
// input through: the markdown chunker, the go/ast path, and a sample of
// the regex-fallback languages (one per distinct pattern shape in
// langPatterns, plus an unrecognized-language name to exercise
// defaultCodePattern).
var fuzzLangs = []string{"go", "python", "rust", "javascript", "unknown-lang"}

// loadFuzzChunkSeeds reads every regular file under fuzzChunkSeedDir
// (any extension — seeds are raw chunker input, not a structured format)
// as raw bytes, sorted for determinism.
func loadFuzzChunkSeeds(f *testing.F) [][]byte {
	f.Helper()
	entries, err := os.ReadDir(fuzzChunkSeedDir)
	if err != nil {
		f.Fatalf("reading fuzz corpus dir %s: %v", fuzzChunkSeedDir, err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && e.Name() != "README.md" {
			names = append(names, e.Name())
		}
	}
	seeds := make([][]byte, 0, len(names))
	for _, name := range names {
		data, rerr := os.ReadFile(filepath.Join(fuzzChunkSeedDir, name))
		if rerr != nil {
			f.Fatalf("reading fuzz seed %s: %v", name, rerr)
		}
		seeds = append(seeds, data)
	}
	if len(seeds) == 0 {
		f.Fatalf("fuzz corpus dir %s has no seed files", fuzzChunkSeedDir)
	}
	return seeds
}

// chunkerFor builds the Chunker for one of fuzzLangs, mirroring
// ChunkerFor's constructors without going through path-extension lookup
// (FuzzChunk drives every language against every input directly).
func chunkerForLang(lang string) Chunker {
	if lang == "markdown" {
		return &MarkdownChunker{}
	}
	return &CodeChunker{lang: lang}
}

// assertChunksInBounds fails the fuzz run if any chunk's byte range is
// not a valid, in-order subrange of an input of length n.
func assertChunksInBounds(t *testing.T, chunks []Chunk, n int) {
	t.Helper()
	for _, c := range chunks {
		if c.StartByte < 0 || c.StartByte > c.EndByte || c.EndByte > n {
			t.Fatalf("chunk out of bounds: start=%d end=%d, input len=%d", c.StartByte, c.EndByte, n)
		}
	}
}

// FuzzChunk drives MarkdownChunker and CodeChunker (across fuzzLangs) over
// arbitrary bytes. A returned error is always a valid outcome (this
// package's error paths); only a panic, or a chunk whose range escapes
// the input, is a bug.
func FuzzChunk(f *testing.F) {
	for _, seed := range loadFuzzChunkSeeds(f) {
		f.Add(seed)
	}
	f.Add([]byte(nil))
	f.Add([]byte{0})
	f.Add([]byte(strings.Repeat("#", 8192)))
	f.Add([]byte(strings.Repeat("a", 1<<16)))
	f.Add([]byte("```\n# not a heading inside a fence\n```\n# real heading\n"))
	f.Add([]byte{0xff, 0xfe, 0xfd, 0x00, 0x80})
	f.Add([]byte("line1\r\nline2\rline3\nline4\r\r\n"))

	f.Fuzz(func(t *testing.T, data []byte) {
		for _, lang := range append([]string{"markdown"}, fuzzLangs...) {
			ck := chunkerForLang(lang)
			chunks, err := ck.Chunk("fuzz-input", data)
			if err != nil {
				continue
			}
			assertChunksInBounds(t, chunks, len(data))
		}
	})
}
