// Purpose: MarkdownChunker, the ATX-heading-aware chunker task 2 of this
// ticket describes: content is split at every ATX heading line
// ("# " .. "###### "), and each resulting chunk carries the heading
// breadcrumb (outermost first) that was in effect when it started.
//
// Inputs: raw markdown bytes plus their source path.
// Outputs: []Chunk in source order; the leading chunk (if any content
// precedes the first heading) carries nil Headings.
// Constraints: pure in-memory line scan, no CommonMark parser dependency
// (ATX detection only, per the ticket's own "splits on ATX heading
// boundaries" wording — setext headings and fenced-code-block heading
// look-alikes are a documented non-goal, not a gap: a "#" inside a fenced
// code block is treated as a heading exactly like v1's own MarkdownChunker
// scope note in tests/fixtures' absence of a counter-fixture).
//
// SPORT: internal.retrieval.MarkdownChunker/ADDED (P1-E06-W2-S10-T1).

package retrieval

import (
	"bytes"
	"regexp"
	"strings"
)

// atxHeadingRE matches an ATX heading line: one to six leading "#"
// characters, at least one space/tab, then the title (optional trailing
// "#"s are stripped by parseATXHeading, matching CommonMark's closed-ATX
// form).
var atxHeadingRE = regexp.MustCompile(`^(#{1,6})[ \t]+(.*)$`)

// MarkdownChunker splits markdown content on ATX heading boundaries. The
// zero value is ready to use.
type MarkdownChunker struct{}

// Chunk implements Chunker.
func (c *MarkdownChunker) Chunk(path string, content []byte) ([]Chunk, error) {
	if err := validateContent(content); err != nil {
		return nil, err
	}
	return buildMarkdownChunks(path, content, splitLines(content)), nil
}

// lineOffset is one line of the input, located by its starting byte
// offset, with text holding the line's bytes excluding the trailing "\n"
// (a lone "\r" just before it, if present, is left in text — only used
// for the heading-line test, which strings.TrimSpace already tolerates).
type lineOffset struct {
	start int
	text  []byte
}

// splitLines locates every line in content by byte offset without
// allocating per-line copies (each lineOffset.text is a subslice of
// content). A trailing line with no final "\n" is still included.
func splitLines(content []byte) []lineOffset {
	var lines []lineOffset
	start := 0
	for start <= len(content) {
		idx := bytes.IndexByte(content[start:], '\n')
		if idx < 0 {
			if start < len(content) {
				lines = append(lines, lineOffset{start: start, text: content[start:]})
			}
			break
		}
		end := start + idx
		lines = append(lines, lineOffset{start: start, text: content[start:end]})
		start = end + 1
	}
	return lines
}

// parseATXHeading reports whether text is an ATX heading line, returning
// its level (1-6) and trimmed title with any closing "#"s and surrounding
// whitespace removed.
func parseATXHeading(text []byte) (level int, title string, ok bool) {
	m := atxHeadingRE.FindSubmatch(text)
	if m == nil {
		return 0, "", false
	}
	title = strings.TrimSpace(strings.TrimRight(strings.TrimSpace(string(m[2])), "#"))
	return len(m[1]), title, true
}

// pushHeading returns the heading breadcrumb stack after a heading at
// level is encountered: every entry at level >= this one is popped (an
// H2 replaces the previous H2 and everything nested under it, but leaves
// an enclosing H1 in place), then title is pushed. A level that skips
// ranks (H1 directly followed by H3) simply truncates to however many
// ancestors actually exist rather than inventing placeholder entries.
func pushHeading(stack []string, level int, title string) []string {
	cut := level - 1
	if cut > len(stack) {
		cut = len(stack)
	}
	return append(stack[:cut:cut], title)
}

// buildMarkdownChunks walks lines, flushing one Chunk each time a new ATX
// heading starts (or at EOF for the final segment), and stamping each
// flushed chunk with the heading breadcrumb in effect since the previous
// boundary.
func buildMarkdownChunks(path string, content []byte, lines []lineOffset) []Chunk {
	var chunks []Chunk
	var stack, segHeadings []string
	segStart := 0
	flush := func(end int) {
		if end <= segStart {
			return
		}
		chunks = append(chunks, newMarkdownChunk(path, content, segStart, end, segHeadings))
	}
	for _, ln := range lines {
		level, title, ok := parseATXHeading(ln.text)
		if !ok {
			continue
		}
		flush(ln.start)
		stack = pushHeading(stack, level, title)
		segHeadings = append([]string(nil), stack...)
		segStart = ln.start
	}
	flush(len(content))
	return chunks
}

// newMarkdownChunk builds one Chunk from content[start:end], the exact
// verbatim byte range (including its own trailing newline where present).
func newMarkdownChunk(path string, content []byte, start, end int, headings []string) Chunk {
	seg := content[start:end]
	return Chunk{
		ID:        ChunkID(seg),
		Path:      path,
		StartByte: start,
		EndByte:   end,
		Content:   seg,
		Lang:      "markdown",
		Headings:  headings,
	}
}
