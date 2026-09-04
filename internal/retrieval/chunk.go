// Purpose: the shared Chunk/Chunker vocabulary internal/retrieval's ingest
// pipeline exposes to T2 (FTS5 indexing) and T3 (embedding/vector upsert):
// a Chunk carries exactly the bytes and provenance those consumers need,
// and Chunker is the single interface both concrete chunkers (markdown.go,
// code.go) implement so downstream callers never branch on chunker type.
//
// Inputs: raw file content plus its path, handed to a Chunker's Chunk
// method. This package never reads from disk itself — the caller (a
// future ingest-walk ticket) supplies bytes already read.
//
// Outputs: []Chunk in source order, or a *cascade.Error from the frozen
// taxonomy (empty input and unrecognized/binary content are KindInvalidInput;
// an extension no chunker recognizes is KindUnsupported).
//
// Constraints: pure in-memory, no I/O, no network (default unit lane
// forbids importing net regardless); ChunkID (id.go) is the only source of
// truth for a Chunk's ID, so both chunkers call it rather than hashing
// inline.
//
// SPORT: internal.retrieval.Chunk/ADDED (P1-E06-W2-S10-T1).

package retrieval

import (
	"bytes"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Chunk is one content-addressed slice of a source file. ID is computed by
// ChunkID over Chunk's own Content (id.go): identical content at two
// different Paths yields identical IDs, and Path/StartByte/EndByte are
// pure provenance that never feed the hash.
type Chunk struct {
	// ID is the BLAKE3-256 hex digest of Content's canonical form
	// (id.go). Stable across runs, platforms, and Path.
	ID string
	// Path is the source file this chunk was extracted from, exactly as
	// passed to Chunker.Chunk.
	Path string
	// StartByte is Content's starting offset within the original input,
	// inclusive.
	StartByte int
	// EndByte is Content's ending offset within the original input,
	// exclusive.
	EndByte int
	// Content is this chunk's exact byte range from the original input
	// (no re-encoding, no trimming beyond what the chunker's boundary
	// rule itself performs).
	Content []byte
	// Lang identifies the chunk's source language ("markdown", "go",
	// "python", ...), the same name ChunkerFor derived from the file
	// extension.
	Lang string
	// Headings is the ATX heading breadcrumb (outermost first) leading
	// to this chunk, populated only by MarkdownChunker. Nil for every
	// code chunk and for a markdown chunk with no enclosing heading.
	Headings []string
}

// Chunker splits one file's raw content into an ordered slice of Chunks.
// MarkdownChunker and CodeChunker are this package's two implementations;
// ChunkerFor selects between them by path extension.
type Chunker interface {
	// Chunk splits content (the bytes of the file at path) into Chunks in
	// source order. path is carried into each Chunk.Path but is
	// otherwise only consulted for its extension where a chunker's
	// boundary rule depends on it (CodeChunker's language selection).
	Chunk(path string, content []byte) ([]Chunk, error)
}

// codeExtLang maps a recognized source-file extension (lowercase, with the
// leading dot) to the language name CodeChunker stamps onto each Chunk.Lang
// and uses to select its regex fallback (code.go's langPatterns). ".go"
// intentionally is not here as a lookup for the fallback path — go/ast
// handles it directly in code.go, but its lang name is still "go" so
// ChunkerFor and CodeChunker.Chunk agree on the string.
var codeExtLang = map[string]string{
	".go":   "go",
	".js":   "javascript",
	".jsx":  "javascript",
	".mjs":  "javascript",
	".cjs":  "javascript",
	".ts":   "typescript",
	".tsx":  "typescript",
	".py":   "python",
	".rs":   "rust",
	".rb":   "ruby",
	".java": "java",
	".c":    "c",
	".h":    "c",
	".sh":   "bash",
}

// ChunkerFor returns the Chunker that handles path's extension: a
// *MarkdownChunker for ".md"/".mdx", a *CodeChunker for every extension in
// codeExtLang, or a KindUnsupported error for anything else (the caller's
// unrecognized-content error path).
func ChunkerFor(path string) (Chunker, error) {
	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".md" || ext == ".mdx" {
		return &MarkdownChunker{}, nil
	}
	if lang, ok := codeExtLang[ext]; ok {
		return &CodeChunker{lang: lang}, nil
	}
	return nil, cascade.Newf(cascade.KindUnsupported, "retrieval: no chunker recognizes extension %q", ext)
}

// validateContent rejects the two shared error paths every Chunker.Chunk
// implementation checks before doing any real work: empty input, and
// content that is not valid UTF-8 text (this package's definition of
// "binary" — a chunker over source/markdown text has no principled
// boundary rule for arbitrary bytes, and a NUL or an invalid UTF-8
// sequence is conclusive evidence the input is not the text format it
// claims to be).
func validateContent(content []byte) error {
	if len(content) == 0 {
		return cascade.New(cascade.KindInvalidInput, "retrieval: empty input")
	}
	if bytes.IndexByte(content, 0) >= 0 || !utf8.Valid(content) {
		return cascade.New(cascade.KindInvalidInput, "retrieval: binary content is not chunkable")
	}
	return nil
}
