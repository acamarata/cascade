// Purpose: the Markdown footnote form of a CitationSet — the reference
// markers that go inline next to an answer, and the definition block that
// goes underneath it — for the `cascade recall --cite` output and for MCP
// search responses.
//
// Inputs: an assembled CitationSet.
// Outputs: one reference marker per citation plus the definition block.
//
// Constraints: rendering is a pure function of the set. It walks the
// citations slice in order and touches no map, so the same set renders to
// the same bytes on every run and on every platform. It also never renders
// anything the set does not hold: a withheld result left no citation, so
// there is nothing here that could disclose it.
//
// SPORT: internal.retrieval.citations.Render/ADDED (P1-E06-W2-S11-T2).

package citations

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/acamarata/cascade/internal/retrieval/corpus"
)

// scoreDigits is the fixed number of fraction digits a rendered score
// carries. It is fixed rather than adaptive so two renders of the same
// score are byte-identical, and so a score never renders in exponential
// form where a reader would read it as a different number entirely.
const scoreDigits = 3

// untrustedMarker is appended to the definition of any citation whose
// effective TRUST tag is not trusted.
//
// The marker is part of the rendered form rather than left to the
// consumer because the rendered form is what a reader actually sees. A
// citation is an argument for believing something; a citation to content
// the user never vouched for is a weaker argument, and dropping that fact
// at the last step would undo the care taken to carry it accurately
// through fusion and merging.
const untrustedMarker = " [untrusted]"

// Rendered is a CitationSet's Markdown footnote form.
type Rendered struct {
	// Refs holds the inline reference marker for each citation, in the
	// set's own order: Refs[i] belongs to Citations[i]. Footnote numbers
	// are 1-based positions in the set, which is what makes them
	// contiguous; they are deliberately NOT the citations' retrieval
	// ranks, which have gaps wherever a merge or a withheld result
	// removed a row.
	Refs []string
	// Definitions is the definition block: one line per citation in set
	// order, each newline-terminated. Empty for an empty set, so an
	// answer with nothing to cite renders no stray block.
	Definitions string
}

// Render returns set's Markdown footnote form.
//
// The reference form is `[^n]` and the definition form is
// `[^n]: path lines a-b (score: 0.123)`, matching the footnote syntax
// Markdown renderers already understand, so the output is readable as
// plain text and correct when rendered.
//
// Every part of a definition after the marker is omitted when it is not
// known: a source with no line span renders no line clause, and a source
// with no path is identified by the corpus it was authorized from, or by
// its content-addressed chunk id when it has neither. Nothing is
// substituted for a missing field.
func Render(set CitationSet) Rendered {
	out := Rendered{}
	if len(set.Citations) == 0 {
		return out
	}
	out.Refs = make([]string, 0, len(set.Citations))
	var defs strings.Builder
	for i, c := range set.Citations {
		n := i + 1
		out.Refs = append(out.Refs, footnoteRef(n))
		defs.WriteString(definition(n, c))
		defs.WriteString("\n")
	}
	out.Definitions = defs.String()
	return out
}

// footnoteRef renders the inline marker for the n-th citation.
func footnoteRef(n int) string {
	return "[^" + strconv.Itoa(n) + "]"
}

// definition renders one definition line, without its newline.
func definition(n int, c Citation) string {
	var b strings.Builder
	b.WriteString("[^")
	b.WriteString(strconv.Itoa(n))
	b.WriteString("]: ")
	b.WriteString(sourceLabel(c))
	if c.Lines.Known() {
		fmt.Fprintf(&b, " lines %d-%d", c.Lines.Start, c.Lines.End)
	}
	fmt.Fprintf(&b, " (score: %s)", strconv.FormatFloat(c.Score, 'f', scoreDigits, 64))
	if c.Trust != corpus.TrustTrusted {
		b.WriteString(untrustedMarker)
	}
	return b.String()
}

// sourceLabel names the source of c as precisely as c actually knows it.
//
// The fallbacks are ordered by how much they tell a reader who wants to go
// and look: a path is directly checkable, a corpus name says which body of
// content it came from, and the chunk id at least identifies the exact
// content. Each one is a fact the citation holds; none of them is a stand-in
// for a fact it does not.
func sourceLabel(c Citation) string {
	switch {
	case c.Path != "":
		return c.Path
	case c.CorpusID != "":
		return "corpus " + c.CorpusID
	default:
		return "chunk " + c.ChunkID
	}
}
