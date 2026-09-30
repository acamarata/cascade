package build

import (
	"strconv"
	"strings"
	"unicode"

	"github.com/acamarata/cascade/pkg/cascade"
)

// docLine is one parsed line of a markdown doc: its raw text and whether
// it sits inside a fenced code block. Every doctruth rule that needs to
// skip code (claim, and the "outside fenced/inline code" clause of every
// backtick-token rule) walks this shared slice instead of re-parsing
// fences itself (C5: one parser).
type docLine struct {
	text   string
	fenced bool
}

// parseDocLines splits content into docLines, marking every line between a
// ``` / ~~~ fence-open and its matching fence-close (inclusive) as fenced.
// A fence marker's own indentation is ignored; only the leading token
// (``` or ~~~, three or more characters) is matched, mirroring CommonMark.
func parseDocLines(content string) []docLine {
	raw := strings.Split(content, "\n")
	out := make([]docLine, len(raw))
	var fenceMarker string
	inFence := false
	for i, line := range raw {
		trimmed := strings.TrimSpace(line)
		if inFence {
			out[i] = docLine{text: line, fenced: true}
			if strings.HasPrefix(trimmed, fenceMarker) {
				inFence = false
			}
			continue
		}
		if m := fenceOpen(trimmed); m != "" {
			out[i] = docLine{text: line, fenced: true}
			fenceMarker = m
			inFence = true
			continue
		}
		out[i] = docLine{text: line, fenced: false}
	}
	return out
}

func fenceOpen(trimmed string) string {
	for _, tok := range []string{"```", "~~~"} {
		if strings.HasPrefix(trimmed, tok) {
			return tok
		}
	}
	return ""
}

// stripInlineCode returns line with every backtick-delimited inline-code
// span replaced by spaces of the same byte length (so column positions of
// surviving text are unaffected), and the list of spans (their raw
// content, unquoted) found. Fenced lines are never passed here — the
// caller skips them.
func stripInlineCode(line string) (stripped string, spans []string) {
	b := []byte(line)
	i := 0
	for i < len(b) {
		if b[i] != '`' {
			i++
			continue
		}
		j := i + 1
		for j < len(b) && b[j] != '`' {
			j++
		}
		if j >= len(b) {
			break // unterminated backtick: leave the rest alone
		}
		spans = append(spans, string(b[i+1:j]))
		for k := i; k <= j; k++ {
			b[k] = ' '
		}
		i = j + 1
	}
	return string(b), spans
}

// slugify implements GitHub's heading-anchor algorithm: lowercase,
// spaces become '-', every character that is not a unicode letter,
// digit, '-' or '_' is dropped (not replaced), '`' among them.
func slugify(heading string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(heading) {
		switch {
		case r == ' ' || r == '\t':
			b.WriteRune('-')
		case r == '-' || r == '_':
			b.WriteRune(r)
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		}
	}
	return b.String()
}

// heading is one ATX heading (# .. ######) found in a doc, with its
// deduplicated GitHub slug.
type heading struct {
	line int
	text string
	slug string
}

// parseHeadings returns every ATX heading in content, outside fenced code,
// with duplicate slugs suffixed -1, -2, ... in order of appearance
// (GitHub's rule).
func parseHeadings(content string) []heading {
	lines := parseDocLines(content)
	seen := map[string]int{}
	var out []heading
	for i, dl := range lines {
		if dl.fenced {
			continue
		}
		text, ok := atxHeadingText(dl.text)
		if !ok {
			continue
		}
		base := slugify(stripBackticks(text))
		slug := base
		if n, exists := seen[base]; exists {
			n++
			seen[base] = n
			slug = base + "-" + strconv.Itoa(n)
		} else {
			seen[base] = 0
		}
		out = append(out, heading{line: i + 1, text: text, slug: slug})
	}
	return out
}

func atxHeadingText(line string) (string, bool) {
	t := strings.TrimLeft(line, " ")
	if !strings.HasPrefix(t, "#") {
		return "", false
	}
	i := 0
	for i < len(t) && t[i] == '#' {
		i++
	}
	if i == 0 || i > 6 {
		return "", false
	}
	if i < len(t) && t[i] != ' ' && t[i] != '\t' {
		return "", false
	}
	return strings.TrimSpace(strings.TrimRight(t[i:], "#")), true
}

func stripBackticks(s string) string {
	return strings.ReplaceAll(s, "`", "")
}

// computeResult bundles the raw report plus the exact set of files this
// run scanned, so CheckDocTruth can filter New/Fixed to that set.
type computeResult struct {
	report       DocTruthReport
	scannedFiles map[string]bool
}

// computeFindings runs every rule (link, anchor, path, line, symbol, test,
// claim, index, directive) over the expanded argument files (or the whole
// scope) and returns every finding, unfiltered by any baseline.
func computeFindings(repoRoot string, files ...string) (computeResult, []DocFinding, error) {
	scope, err := DocTruthScope(repoRoot)
	if err != nil {
		return computeResult{}, nil, err
	}
	tracked, dirs, err := trackedSets(repoRoot)
	if err != nil {
		return computeResult{}, nil, err
	}
	targets, argDirs, err := expandArgs(scope, files)
	if err != nil {
		return computeResult{}, nil, err
	}
	if len(targets) == 0 {
		return computeResult{}, nil, cascade.New(cascade.KindNotFound, "doctruth: zero files scanned")
	}
	k := newKeyer()
	rep, findings, scanned, err := scanTargets(repoRoot, targets, tracked, dirs, k)
	if err != nil {
		return computeResult{}, nil, err
	}
	applies := func(indexPage string) bool {
		if len(files) == 0 {
			return true
		}
		if scanned[indexPage] {
			return true
		}
		for d := range argDirs {
			if indexPage == d || strings.HasPrefix(indexPage, d+"/") {
				return true
			}
		}
		return false
	}
	idxFindings, err := checkIndexes(repoRoot, tracked, applies, k)
	if err != nil {
		return computeResult{}, nil, err
	}
	findings = append(findings, idxFindings...)
	for _, spec := range docIndexSpecs {
		if applies(spec.primary) {
			scanned[spec.primary] = true
		}
	}
	return computeResult{report: rep, scannedFiles: scanned}, findings, nil
}

// scanTargets runs the link/anchor, path/line/symbol/test/directive, and
// claim rules over every target page, accumulating the report counters
// and every finding. Split out of computeFindings to stay under
// Art.10.3's 50-line function cap.
func scanTargets(repoRoot string, targets []string, tracked, dirs map[string]bool, k *keyer) (DocTruthReport, []DocFinding, map[string]bool, error) {
	hc := newHeadingCache(repoRoot)
	refr := newRefResolver(repoRoot, tracked, dirs)
	scanned := map[string]bool{}
	var findings []DocFinding
	rep := DocTruthReport{}
	for _, page := range targets {
		scanned[page] = true
		data, err := readFileFn(pathJoin(repoRoot, page))
		if err != nil {
			return DocTruthReport{}, nil, nil, wrapReadErr("doctruth: reading doc", page, err)
		}
		content := string(data)
		rep.Files++
		lf, nlinks, err := checkLinksAndAnchors(page, content, tracked, dirs, hc, k)
		if err != nil {
			return DocTruthReport{}, nil, nil, err
		}
		findings = append(findings, lf...)
		rep.Links += nlinks
		rf, nrefs, err := refr.checkRefs(page, content, k)
		if err != nil {
			return DocTruthReport{}, nil, nil, err
		}
		findings = append(findings, rf...)
		rep.Refs += nrefs
		findings = append(findings, checkClaims(page, content, k)...)
		for _, f := range rf {
			if f.Rule == DocRuleDirective {
				rep.Directives++
			}
		}
	}
	return rep, findings, scanned, nil
}
