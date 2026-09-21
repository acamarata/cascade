// Purpose: the R-21.191 artifact filter (P1-E25-W5-S52-T4, CR fix D7): the
//   unified-diff section splitter every reviewer dispatch's artifact passes
//   through, the exclusion rule for author-written `.claude/**`, AGENTS.md
//   and generated projections, and the FAIL-CLOSED format check -- an
//   artifact whose format this file cannot recognise is REFUSED, never
//   passed through unfiltered.
// Inputs: the caller's provider.ReviewRequest.Diff (and, for the softer
//   section pass, its free-text Context).
// Outputs: FilterArtifact -> (filtered diff, excluded paths, error);
//   filterDiffSections -> (filtered text, excluded paths) with no format
//   requirement, for free text that MAY contain diff sections.
// Constraints: three header dialects are recognised, because three are what
//   real tools emit: `diff --git a/X b/X` (git, prefixed), `diff --git X Y`
//   (git --no-prefix), and a bare `--- old` / `+++ new` pair (diff -u, and
//   git's own body lines). A bare `@@` hunk with no header names no path,
//   so it cannot be filtered and is refused rather than waved through.
//   A git-QUOTED header path is C-style unquoted before the segment rule
//   runs, so `"a/\303\251/AGENTS.md"` is still an AGENTS.md file. Content
//   BEFORE the first recognised header names no path at all and is refused
//   (errArtifactPreamble) rather than dispatched unfiltered.
//   "generated projection" matches a path SEGMENT named `generated` or the
//   Go toolchain's own `// Code generated ... DO NOT EDIT.` marker inside
//   the section -- never an arbitrary substring, so internal/codegen/
//   generated_api.go is NOT excluded (CR #10's bypass input).
// SPORT: internal/review.artifact/ADD (P1-E25-W5-S52-T4).

package review

import (
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// ErrUnrecognisedArtifactFormat is returned by FilterArtifact when the
// artifact carries no unified-diff file header this package can attribute
// to a path. Passing such an artifact through would mean dispatching
// content the R-21.191 exclusion filter never inspected, so this fails
// CLOSED. The message names the expected dialects and never echoes the
// artifact's own content.
var ErrUnrecognisedArtifactFormat = cascade.New(cascade.KindInvalidInput,
	"internal/review: unrecognised artifact format -- no unified-diff file header found "+
		"(expected `diff --git a/X b/X`, `diff --git X Y` from --no-prefix, or a `--- old`/`+++ new` pair); "+
		"an artifact whose paths cannot be attributed cannot be filtered for R-21.191 exclusions, so it is refused")

// errArtifactPreamble is the refusal for an artifact carrying content BEFORE
// its first recognised diff header. Such lines name no path, so excluded()
// can never drop them, and the draft still counted the artifact as
// attributable on the strength of the sections that followed: a
// "REVIEWER BRIEFING (from .claude/CLAUDE.md)" preamble stapled in front of a
// valid `diff --git` body reached the model unfiltered, with no note -- the
// R-21.191 self-briefing channel the confirming review found open. It WRAPS
// ErrUnrecognisedArtifactFormat (the R-21.217 rule crc.go's refusal follows)
// so the sentinel stays reachable by identity, and names the cause.
func errArtifactPreamble() error {
	return cascade.Wrap(cascade.KindInvalidInput, ErrUnrecognisedArtifactFormat,
		"internal/review: the artifact carries content before the first diff header; that content names no path, "+
			"so it cannot be checked against the R-21.191 exclusion list, and the review is refused rather than "+
			"dispatched with unfiltered non-diff content")
}

// goGeneratedMarker is the Go toolchain's own generated-file header, the
// one marker this tree can point at for "generated projection" (the diff
// body carries it with a leading `+` or context space).
var goGeneratedMarker = regexp.MustCompile(`(?m)^[+\- ]?// Code generated .* DO NOT EDIT\.$`)

// gitHeader matches both git dialects in one pattern: the two path tokens
// after `diff --git`, whether or not they carry the a/ b/ prefixes. A token
// may be git-QUOTED ("a/x y/z", "a/\303\251/z" -- core.quotePath is on by
// default), which is why the quoted alternative comes first: a bare \S+ stops
// at the first space and would hand the rule half a path.
var gitHeader = regexp.MustCompile(`^diff --git\s+("(?:[^"\\]|\\.)*"|\S+)\s+("(?:[^"\\]|\\.)*"|\S+)`)

// plainDiffHeader matches `diff -u old new`, `diff -ru old new` and their
// relatives: a short-flag diff invocation line rather than a git one.
var plainDiffHeader = regexp.MustCompile(`^diff\s+-[A-Za-z]+\s+(\S+)\s+(\S+)`)

// diffSection is one file's worth of a unified diff: every line of it, and
// every path its headers named.
type diffSection struct {
	paths   []string
	lines   []string
	sawPlus bool
	sawHunk bool
	// preamble marks the path-less LEADING section: lines that appeared
	// before any recognised header. Only the first section can be one.
	preamble bool
}

// blank reports whether every line of s is whitespace -- the one leading
// section that is not a briefing preamble.
func (s *diffSection) blank() bool {
	return strings.TrimSpace(strings.Join(s.lines, "\n")) == ""
}

// cleanPath normalises one header path token: the a/ b/ prefixes git adds,
// and the trailing tab-separated timestamp `diff -u` appends. It reports
// ok=false for /dev/null and for an empty token, neither of which names a
// file whose content could be excluded.
func cleanPath(tok string) (string, bool) {
	tok = strings.TrimSpace(tok)
	if strings.HasPrefix(tok, `"`) {
		tok = unquoteGitPath(tok)
	} else if i := strings.IndexByte(tok, '\t'); i >= 0 {
		tok = tok[:i]
	}
	if tok == "" || tok == "/dev/null" {
		return "", false
	}
	tok = strings.TrimPrefix(tok, "a/")
	tok = strings.TrimPrefix(tok, "b/")
	return tok, true
}

// unquoteGitPath C-style unquotes one git-quoted path token. git quotes a
// path containing a space, a quote, a backslash or a non-ASCII byte
// (core.quotePath, on by default) and escapes the non-ASCII bytes in octal:
// `"a/\303\251/AGENTS.md"`. Left quoted, the last segment reads `AGENTS.md"`
// and the exact-filename rule misses it (the confirming review's note), so
// the token is unquoted BEFORE the segment rule runs. strconv.Unquote speaks
// the same escapes for the cases git emits; when it cannot, the surrounding
// quotes are stripped anyway, which keeps the rule matching rather than
// letting a malformed quote bypass it.
func unquoteGitPath(tok string) string {
	if unquoted, err := strconv.Unquote(tok); err == nil {
		return unquoted
	}
	return strings.Trim(tok, `"`)
}

// isExcludedPath applies R-21.191's exclusion list by path SEGMENT: a
// `.claude` or `generated` directory anywhere in the path, or a file named
// AGENTS.md at any depth. A substring match is deliberately NOT used --
// `internal/codegen/generated_api.go` contains "generated" and is ordinary
// source (CR #10).
func isExcludedPath(path string) bool {
	segs := strings.Split(path, "/")
	for i, s := range segs {
		if s == ".claude" || s == "generated" {
			return true
		}
		if i == len(segs)-1 && s == "AGENTS.md" {
			return true
		}
	}
	return false
}

// startsSection reports whether line opens a new file section, and the
// paths that header names. lines and i allow lookahead: a bare `--- ` or
// `diff -u` line names a path standing alone, but a real header is ALWAYS
// followed immediately by its `+++ new` counterpart -- without that check
// a single unpaired line (e.g. a briefing preamble starting `--- `) was
// accepted as an attributable header and dispatched unfiltered.
func startsSection(line string, cur *diffSection, lines []string, i int) (paths []string, ok bool) {
	if m := gitHeader.FindStringSubmatch(line); m != nil {
		return cleanPair(m[1], m[2]), true
	}
	if m := plainDiffHeader.FindStringSubmatch(line); m != nil {
		if !nextLinesAreDiffPair(lines, i+1) {
			return nil, false
		}
		return cleanPair(m[1], m[2]), true
	}
	// A bare `--- old` opens a section only when the current one is
	// already past its own header pair (inside a git section the `--- a/x`
	// body line belongs to the header the `diff --git` line opened) AND the
	// very next line is its `+++ new` counterpart.
	if strings.HasPrefix(line, "--- ") && (cur == nil || cur.sawPlus || cur.sawHunk) {
		if i+1 >= len(lines) || !strings.HasPrefix(lines[i+1], "+++ ") {
			return nil, false
		}
		return cleanPair(strings.TrimPrefix(line, "--- "), ""), true
	}
	return nil, false
}

// nextLinesAreDiffPair reports whether lines[i]/[i+1] are a `--- old` /
// `+++ new` pair, the shape a real `diff -u` line always emits next.
func nextLinesAreDiffPair(lines []string, i int) bool {
	if i < 0 || i+1 >= len(lines) {
		return false
	}
	return strings.HasPrefix(lines[i], "--- ") && strings.HasPrefix(lines[i+1], "+++ ")
}

// cleanPair cleans up to two header tokens, dropping the ones that name no
// real file.
func cleanPair(a, b string) []string {
	out := make([]string, 0, 2)
	for _, tok := range []string{a, b} {
		if p, ok := cleanPath(tok); ok {
			out = append(out, p)
		}
	}
	return out
}

// splitSections walks diff line by line and groups it into file sections.
// Lines before the first header (a preamble a tool may emit) form a
// path-less leading section, which carries no attributable path and is
// therefore never excluded -- and never counts toward recognition.
func splitSections(diff string) []*diffSection {
	lines := strings.Split(diff, "\n")
	var out []*diffSection
	var cur *diffSection
	for i, line := range lines {
		if paths, ok := startsSection(line, cur, lines, i); ok {
			cur = &diffSection{paths: paths}
			out = append(out, cur)
			cur.lines = append(cur.lines, line)
			continue
		}
		if cur == nil {
			cur = &diffSection{preamble: true}
			out = append(out, cur)
		}
		switch {
		case strings.HasPrefix(line, "+++ "):
			cur.sawPlus = true
			if p, ok := cleanPath(strings.TrimPrefix(line, "+++ ")); ok {
				cur.paths = append(cur.paths, p)
			}
		case strings.HasPrefix(line, "@@"):
			cur.sawHunk = true
		}
		cur.lines = append(cur.lines, line)
	}
	return out
}

// excluded reports whether s must be dropped, and the path to name in the
// note. A section is excluded when any path it names is excluded, or when
// its own body carries the Go generated-file marker.
func (s *diffSection) excluded() (string, bool) {
	for _, p := range s.paths {
		if isExcludedPath(p) {
			return p, true
		}
	}
	if len(s.paths) > 0 && goGeneratedMarker.MatchString(strings.Join(s.lines, "\n")) {
		return s.paths[0], true
	}
	return "", false
}

// filterDiffSections drops every excluded file section and reports the
// paths it dropped. It imposes NO format requirement: text that contains
// no diff header at all comes back byte-identical, which is what the
// caller's free-text Context needs (it is background prose, not the
// artifact under review).
func filterDiffSections(text string) (string, []string) {
	if text == "" {
		return "", nil
	}
	sections := splitSections(text)
	kept := make([]string, 0, len(sections))
	seen := map[string]bool{}
	var dropped []string
	for _, s := range sections {
		if path, drop := s.excluded(); drop {
			if !seen[path] {
				seen[path] = true
				dropped = append(dropped, path)
			}
			continue
		}
		kept = append(kept, strings.Join(s.lines, "\n"))
	}
	sort.Strings(dropped)
	return strings.Join(kept, "\n"), dropped
}

// FilterArtifact is filterDiffSections plus the FAIL-CLOSED format check
// the artifact under review must satisfy: at least one section whose header
// names a real path. An artifact with none is refused with
// ErrUnrecognisedArtifactFormat rather than dispatched unfiltered (D7).
func FilterArtifact(diff string) (string, []string, error) {
	attributable := false
	for _, s := range splitSections(diff) {
		if s.preamble && !s.blank() {
			return "", nil, errArtifactPreamble()
		}
		if len(s.paths) > 0 {
			attributable = true
		}
	}
	if !attributable {
		return "", nil, ErrUnrecognisedArtifactFormat
	}
	filtered, dropped := filterDiffSections(diff)
	return filtered, dropped, nil
}
