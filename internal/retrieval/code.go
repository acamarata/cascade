// Purpose: CodeChunker, task 3 of this ticket: go/ast-based chunking for
// ".go" files (one chunk per top-level declaration, doc comment
// included), and a regex-based top-level-declaration splitter for every
// other extension codeExtLang (chunk.go) recognizes.
//
// Inputs: raw source bytes plus their path (path's extension selects the
// language when the CodeChunker was not already constructed with one via
// ChunkerFor).
// Outputs: []Chunk in source order.
// Constraints: the regex fallback is a best-effort top-level-boundary
// splitter, not a parser — it recognizes the common declaration keyword
// at column zero for each language and cannot see through nested braces,
// which is the accepted tradeoff the ticket's own wording ("regex-based
// top-level declaration splitter") describes; go/ast is used for ".go"
// specifically because a real parser is available and free of that
// tradeoff.
//
// SPORT: internal.retrieval.CodeChunker/ADDED (P1-E06-W2-S10-T1).

package retrieval

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// langPatterns holds the top-level-declaration regex for every non-Go
// language in codeExtLang. Each pattern matches at the start of a line
// (no leading whitespace — Art.10.2-style "top level" by convention
// across these languages) against one of that language's declaration
// keywords.
var langPatterns = map[string]*regexp.Regexp{
	"javascript": regexp.MustCompile(`^(export\s+)?(default\s+)?(async\s+)?(function\b|class\b)`),
	"typescript": regexp.MustCompile(`^(export\s+)?(default\s+)?(async\s+)?(function\b|class\b|interface\b|type\b|enum\b)`),
	"python":     regexp.MustCompile(`^(async\s+)?def\b|^class\b`),
	"rust":       regexp.MustCompile(`^(pub(\([^)]*\))?\s+)?(async\s+)?(fn\b|struct\b|enum\b|impl\b|trait\b|mod\b)`),
	"ruby":       regexp.MustCompile(`^def\b|^class\b|^module\b`),
	"java":       regexp.MustCompile(`^(public\s+|private\s+|protected\s+)?(static\s+)?(final\s+)?(class\b|interface\b|enum\b)`),
	"c":          regexp.MustCompile(`^[A-Za-z_][\w *]*\s\**\w+\s*\([^;]*\)\s*\{?\s*$`),
	"bash":       regexp.MustCompile(`^(function\s+\w+|[\w-]+\s*\(\)\s*\{)`),
}

// defaultCodePattern is used for any language in codeExtLang with no
// entry in langPatterns, covering the declaration keywords common across
// C-family and scripting languages.
var defaultCodePattern = regexp.MustCompile(`^(func|function|class|def|struct|interface|type|impl|trait)\b`)

// CodeChunker splits source content into one chunk per top-level
// declaration: go/ast for ".go" (lang == "go"), a per-language regex
// fallback (langPatterns) otherwise. The zero value derives its language
// from each call's path extension; ChunkerFor instead constructs one with
// lang already set.
type CodeChunker struct {
	lang string
}

// Chunk implements Chunker.
func (c *CodeChunker) Chunk(path string, content []byte) ([]Chunk, error) {
	if err := validateContent(content); err != nil {
		return nil, err
	}
	lang := c.lang
	if lang == "" {
		lang = codeExtLang[strings.ToLower(filepath.Ext(path))]
	}
	if lang == "go" {
		return chunkGoSource(path, content)
	}
	return chunkByRegex(path, content, lang), nil
}

// chunkGoSource parses content as Go source and returns one Chunk per
// top-level declaration, each including its own leading doc comment (if
// any). A file with no top-level declarations at all (package clause and
// imports only) is returned as a single chunk. A parse error is reported
// as a KindInvalidInput taxonomy error, never a panic.
func chunkGoSource(path string, content []byte) ([]Chunk, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, content, parser.ParseComments)
	if err != nil {
		return nil, cascade.Wrapf(cascade.KindInvalidInput, err, "retrieval: parsing go source %q", path)
	}
	if len(file.Decls) == 0 {
		return []Chunk{newCodeChunk(path, content, 0, len(content), "go")}, nil
	}
	chunks := make([]Chunk, 0, len(file.Decls))
	for _, decl := range file.Decls {
		start := fset.Position(declStart(decl)).Offset
		end := fset.Position(decl.End()).Offset
		if end > len(content) {
			end = len(content)
		}
		chunks = append(chunks, newCodeChunk(path, content, start, end, "go"))
	}
	return chunks, nil
}

// declStart returns the position a top-level declaration's chunk should
// start at: its doc comment's position when present, otherwise the
// declaration itself.
func declStart(decl ast.Decl) token.Pos {
	switch d := decl.(type) {
	case *ast.GenDecl:
		if d.Doc != nil {
			return d.Doc.Pos()
		}
	case *ast.FuncDecl:
		if d.Doc != nil {
			return d.Doc.Pos()
		}
	}
	return decl.Pos()
}

// chunkByRegex splits content at every line matching lang's top-level
// pattern (langPatterns, falling back to defaultCodePattern), the same
// leading-segment behavior as MarkdownChunker: any content before the
// first match (imports, a header comment) becomes its own leading chunk.
func chunkByRegex(path string, content []byte, lang string) []Chunk {
	pat, ok := langPatterns[lang]
	if !ok {
		pat = defaultCodePattern
	}
	var chunks []Chunk
	segStart := 0
	flush := func(end int) {
		if end <= segStart {
			return
		}
		chunks = append(chunks, newCodeChunk(path, content, segStart, end, lang))
	}
	for _, ln := range splitLines(content) {
		if !pat.Match(ln.text) {
			continue
		}
		flush(ln.start)
		segStart = ln.start
	}
	flush(len(content))
	return chunks
}

// newCodeChunk builds one Chunk from content[start:end]. Code chunks
// never carry Headings (that field is markdown-only).
func newCodeChunk(path string, content []byte, start, end int, lang string) Chunk {
	seg := content[start:end]
	return Chunk{
		ID:        ChunkID(seg),
		Path:      path,
		StartByte: start,
		EndByte:   end,
		Content:   seg,
		Lang:      lang,
	}
}
