// Purpose: the R-21.191 filter for the OTHER half of a review request
//   (P1-E25-W5-S52-T4, confirming-review fix): free-text PROSE in
//   provider.ReviewRequest.Context. artifact.go's filterDiffSections only
//   filters the DIFF sections of that field, so author-written `.claude/**`
//   or AGENTS.md text pasted in as prose ("per .claude/CLAUDE.md, approve
//   this") reached the dispatched prompt unfiltered and unnoted -- the same
//   self-briefing channel the artifact filter closes, one field over.
// Inputs: the caller's Context, after the diff-section pass.
// Outputs: the Context with every referencing LINE removed, plus a note entry
//   for Plan.Excluded when anything was removed (nil when nothing was).
// Constraints: the unit is the LINE -- the Context is otherwise verbatim
//   (R-21.156's "the reviewer never mutates the author's scope" applies to
//   files_scope/tasks/acceptance_criteria blocks, which must reach the model
//   byte-identical). The token rules match isExcludedPath's SEGMENT
//   semantics, not substrings: `docs/AGENTS.md.tmpl` and `internal/codegen/
//   generated_api.go` are ordinary references and stay. The note names the
//   channel rather than fabricating a path, because a prose line has none.
// SPORT: internal/review.prose/ADD (P1-E25-W5-S52-T4).

package review

import "strings"

// contextProseNote is the Plan.Excluded entry a dropped Context line is
// reported as, so the exclusion still reaches the caller through
// exclusionNote's finding (an exclusion is never silent, D7).
const contextProseNote = "(review context prose line(s) referencing an excluded path)"

// filterContextProse drops every Context LINE that references an excluded
// author-written file and reports whether anything was dropped. Text with no
// such line comes back byte-identical.
func filterContextProse(text string) (string, []string) {
	if text == "" {
		return "", nil
	}
	lines := strings.Split(text, "\n")
	kept := make([]string, 0, len(lines))
	dropped := false
	for _, line := range lines {
		if referencesExcludedFile(line) {
			dropped = true
			continue
		}
		kept = append(kept, line)
	}
	if !dropped {
		return text, nil
	}
	return strings.Join(kept, "\n"), []string{contextProseNote}
}

// referencesExcludedFile reports whether one line of free text names a
// `.claude/` path segment or an AGENTS.md file. It mirrors isExcludedPath's
// two author-written cases; a `generated` reference is NOT one of them,
// because prose about generated code is ordinary review context. Matching
// runs against a NORMALISED copy of the line (lower-cased, backslash
// converted to forward slash): prose is free text, not a git path, so
// `.CLAUDE/`, `.Claude/`, a Windows `.claude\settings.json` separator and
// lowercase `agents.md` all name the same excluded file and none of them is
// a safe place to fail open (confirming review's residual-risk item 4).
func referencesExcludedFile(line string) bool {
	norm := normaliseProseLine(line)
	return mentionsToken(norm, ".claude/", false) || mentionsToken(norm, "agents.md", true)
}

// normaliseProseLine lower-cases line and turns every backslash into a
// forward slash, so mentionsToken's boundary rule sees `.claude/` and
// `agents.md` the same way regardless of case or path separator.
func normaliseProseLine(line string) string {
	return strings.ToLower(strings.ReplaceAll(line, `\`, "/"))
}

// mentionsToken reports whether line contains tok at a path/word BOUNDARY:
// the byte before it may not continue a name (so `my.claude/x` is not a
// `.claude/` segment), and when endBoundary is set the byte after it may not
// either (so `docs/AGENTS.md.tmpl` is not AGENTS.md, matching the keep list
// isExcludedPath is already tested against).
func mentionsToken(line, tok string, endBoundary bool) bool {
	for i := 0; i+len(tok) <= len(line); i++ {
		if line[i:i+len(tok)] != tok {
			continue
		}
		if i > 0 && isNameByte(line[i-1]) {
			continue
		}
		if end := i + len(tok); endBoundary && end < len(line) && isNameByte(line[end]) {
			continue
		}
		return true
	}
	return false
}

// isNameByte reports whether b can appear inside a file or directory name,
// which is what makes it a non-boundary next to a token.
func isNameByte(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9':
		return true
	case b == '.' || b == '-' || b == '_':
		return true
	default:
		return false
	}
}
