// Purpose: unit tests for chunk.go/id.go/markdown.go/code.go covering the
// ticket's own acceptance criteria: md/code chunking correctness, ID
// stability (path-independence, CRLF/LF equivalence, whitespace-change
// sensitivity), error paths (empty input, binary content), and the golden
// fixtures under testdata/v1-goldens/.
//
// SPORT: internal.retrieval.Chunk/ADDED (P1-E06-W2-S10-T1) — test coverage.
package retrieval

import (
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// --- markdown chunking correctness ---------------------------------------

func TestMarkdownChunker_HeadingHierarchy(t *testing.T) {
	src := "intro text\n# H1\nbody1\n## H2a\nbody2a\n### H3\nbody3\n## H2b\nbody2b\n"
	chunks, err := (&MarkdownChunker{}).Chunk("doc.md", []byte(src))
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}
	want := [][]string{
		nil,
		{"H1"},
		{"H1", "H2a"},
		{"H1", "H2a", "H3"},
		{"H1", "H2b"},
	}
	if len(chunks) != len(want) {
		t.Fatalf("got %d chunks, want %d: %+v", len(chunks), len(want), chunks)
	}
	for i, c := range chunks {
		if strings.Join(c.Headings, "/") != strings.Join(want[i], "/") {
			t.Errorf("chunk %d headings = %v, want %v", i, c.Headings, want[i])
		}
		if c.Lang != "markdown" {
			t.Errorf("chunk %d lang = %q, want markdown", i, c.Lang)
		}
	}
}

func TestMarkdownChunker_ContiguousByteRanges(t *testing.T) {
	src := "# A\nfoo\n## B\nbar\n"
	chunks, err := (&MarkdownChunker{}).Chunk("doc.md", []byte(src))
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}
	if chunks[0].StartByte != 0 || chunks[len(chunks)-1].EndByte != len(src) {
		t.Fatalf("byte ranges don't cover the input: %+v", chunks)
	}
	for i := 1; i < len(chunks); i++ {
		if chunks[i-1].EndByte != chunks[i].StartByte {
			t.Errorf("gap/overlap between chunk %d (end %d) and %d (start %d)",
				i-1, chunks[i-1].EndByte, i, chunks[i].StartByte)
		}
	}
}

func TestMarkdownChunker_NoHeadings(t *testing.T) {
	src := "just a paragraph, no headings at all.\n"
	chunks, err := (&MarkdownChunker{}).Chunk("doc.md", []byte(src))
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}
	if len(chunks) != 1 || chunks[0].Headings != nil {
		t.Fatalf("got %+v, want one chunk with nil Headings", chunks)
	}
}

// --- code chunking correctness -------------------------------------------

func TestCodeChunker_GoTopLevelDecls(t *testing.T) {
	src := "package p\n\n// Doc for F.\nfunc F() {}\n\ntype T struct{}\n\nvar V = 1\n"
	chunks, err := (&CodeChunker{lang: "go"}).Chunk("p.go", []byte(src))
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}
	if len(chunks) != 3 {
		t.Fatalf("got %d chunks, want 3 (func, type, var): %+v", len(chunks), chunks)
	}
	if !strings.Contains(string(chunks[0].Content), "func F()") {
		t.Errorf("chunk 0 missing func F, or doc comment not attached: %q", chunks[0].Content)
	}
	for _, c := range chunks {
		if c.Lang != "go" {
			t.Errorf("chunk lang = %q, want go", c.Lang)
		}
	}
}

func TestCodeChunker_GoParseError(t *testing.T) {
	_, err := (&CodeChunker{lang: "go"}).Chunk("p.go", []byte("package p\nfunc ((("))
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("got %v, want KindInvalidInput", err)
	}
}

func TestCodeChunker_RegexFallback_Python(t *testing.T) {
	src := "import os\n\ndef f():\n    pass\n\nclass C:\n    pass\n"
	chunks, err := (&CodeChunker{lang: "python"}).Chunk("m.py", []byte(src))
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}
	if len(chunks) != 3 {
		t.Fatalf("got %d chunks, want 3 (preamble, def, class): %+v", len(chunks), chunks)
	}
	if !strings.HasPrefix(string(chunks[1].Content), "def f():") {
		t.Errorf("chunk 1 = %q, want to start with def f()", chunks[1].Content)
	}
	if !strings.HasPrefix(string(chunks[2].Content), "class C:") {
		t.Errorf("chunk 2 = %q, want to start with class C:", chunks[2].Content)
	}
}

func TestMarkdownChunker_HeadingLevelSkip(t *testing.T) {
	// H1 immediately followed by H3 (no H2 in between): pushHeading's
	// cut > len(stack) branch, which truncates to the ancestors that
	// actually exist rather than inventing a placeholder H2.
	src := "# H1\nbody\n### H3\nbody2\n"
	chunks, err := (&MarkdownChunker{}).Chunk("doc.md", []byte(src))
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}
	if len(chunks) != 2 {
		t.Fatalf("got %d chunks, want 2: %+v", len(chunks), chunks)
	}
	if strings.Join(chunks[1].Headings, "/") != "H1/H3" {
		t.Errorf("chunk 1 headings = %v, want [H1 H3]", chunks[1].Headings)
	}
}

func TestCodeChunker_ZeroValueDerivesLangFromPath(t *testing.T) {
	src := "def f():\n    pass\n"
	chunks, err := (&CodeChunker{}).Chunk("m.py", []byte(src))
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}
	if len(chunks) != 1 || chunks[0].Lang != "python" {
		t.Fatalf("got %+v, want one python chunk", chunks)
	}
}

func TestCodeChunker_GoNoTopLevelDecls(t *testing.T) {
	chunks, err := (&CodeChunker{lang: "go"}).Chunk("p.go", []byte("package p\n"))
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}
	if len(chunks) != 1 || chunks[0].Lang != "go" {
		t.Fatalf("got %+v, want one whole-file go chunk", chunks)
	}
}

func TestCodeChunker_RegexFallback_UnknownLangUsesDefaultPattern(t *testing.T) {
	src := "preamble\nfunc f() {}\n"
	chunks, err := (&CodeChunker{lang: "some-unrecognized-lang"}).Chunk("m.xyz", []byte(src))
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}
	if len(chunks) != 2 {
		t.Fatalf("got %d chunks, want 2 (preamble, func): %+v", len(chunks), chunks)
	}
}

func TestChunkerFor(t *testing.T) {
	cases := []struct {
		path    string
		wantErr bool
	}{
		{"a.md", false},
		{"a.mdx", false},
		{"a.go", false},
		{"a.py", false},
		{"a.unknownext", true},
	}
	for _, c := range cases {
		_, err := ChunkerFor(c.path)
		if (err != nil) != c.wantErr {
			t.Errorf("ChunkerFor(%q) err = %v, wantErr %v", c.path, err, c.wantErr)
		}
		if c.wantErr && !cascade.HasKind(err, cascade.KindUnsupported) {
			t.Errorf("ChunkerFor(%q) kind = %v, want KindUnsupported", c.path, err)
		}
	}
}

// --- ID stability ----------------------------------------------------------

func TestChunkID_PathIndependent(t *testing.T) {
	content := []byte("identical content, different paths\n")
	c1, err := (&MarkdownChunker{}).Chunk("a/one.md", content)
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}
	c2, err := (&MarkdownChunker{}).Chunk("b/two.md", content)
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}
	if c1[0].ID != c2[0].ID {
		t.Fatalf("same content at different paths got different IDs: %q vs %q", c1[0].ID, c2[0].ID)
	}
	if c1[0].Path == c2[0].Path {
		t.Fatalf("test setup bug: paths should differ")
	}
}

func TestChunkID_CRLFStable(t *testing.T) {
	lf := []byte("line one\nline two\n")
	crlf := []byte("line one\r\nline two\r\n")
	if ChunkID(lf) != ChunkID(crlf) {
		t.Fatalf("LF and CRLF of the same content produced different IDs: %s vs %s", ChunkID(lf), ChunkID(crlf))
	}
}

func TestChunkID_WhitespaceChangeProducesNewID(t *testing.T) {
	original := []byte("line one\nline two\n")
	changed := []byte("line one \nline two\n") // trailing space added on line one
	if ChunkID(original) == ChunkID(changed) {
		t.Fatalf("a meaningful whitespace change did not change the ID")
	}
}

func TestChunkID_SameTwiceDeterministic(t *testing.T) {
	content := []byte("hash me twice\n")
	first := ChunkID(content)
	second := ChunkID(append([]byte(nil), content...)) // distinct backing array, same bytes
	if first != second {
		t.Fatalf("ChunkID is not deterministic across calls: %s vs %s", first, second)
	}
}

// --- error paths ------------------------------------------------------------

func TestChunkers_EmptyInput(t *testing.T) {
	for _, ck := range []Chunker{&MarkdownChunker{}, &CodeChunker{lang: "go"}} {
		_, err := ck.Chunk("x", nil)
		if !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Errorf("%T empty input: got %v, want KindInvalidInput", ck, err)
		}
	}
}

func TestChunkers_BinaryContent(t *testing.T) {
	binary := []byte{0x00, 0x01, 0x02, 0xff, 0xfe, 0x00}
	for _, ck := range []Chunker{&MarkdownChunker{}, &CodeChunker{lang: "go"}} {
		_, err := ck.Chunk("x", binary)
		if !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Errorf("%T binary input: got %v, want KindInvalidInput", ck, err)
		}
	}
}

// Golden-fixture tests (testdata/v1-goldens/) live in chunk_golden_test.go,
// split out to keep this file under Art.10.3's 300-line cap.
