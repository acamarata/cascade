package runtime

// Purpose: the line-classification state machine behind toml_edit.go's
//
//	SetKeyLine/UnsetKeyLine — walks a TOML document's lines tracking the
//	current `[table.path]` context, classifies each line as a table
//	header, a key=value assignment, or other (comment/blank/anything this
//	editor does not attempt to parse), and answers the two questions the
//	editor needs: "which line (if any) assigns dotted's value?" and
//	"where does tablePath's block end, for an insert?" Split out of
//	toml_edit.go per R-14.117/Art.10.3 (task 2's anticipated split) and
//	06-FORGE-SPEC §5.7 (FuzzTomlEdit exercises this file's parsing via
//	toml_edit.go's public entry points).
//
// Inputs: a TOML document already split into lines (toml_edit.go's
//
//	splitLines).
//
// Outputs: a lineScan the editor queries by dotted path or table path.
// Constraints: single-line values only — a value spanning multiple lines
//
//	(a multi-line basic/literal string, or an array/inline-table broken
//	across lines) is not matched by findKey and therefore never edited in
//	place; SetKeyLine's fallback (insert-new-line / append-new-table)
//	still produces a correct, additive edit in that case, it just does
//	not rewrite the pre-existing multi-line value — a documented
//	limitation, not a crash or data-loss bug (see toml_edit.go's package
//	doc). `[[array-of-tables]]` headers are recognised only well enough
//	to not be misread as a plain `[table]` header (never treated as a
//	settable table context by this editor). The atomic-write primitive
//	(writeBytesAtomic/WriteBytesAtomic/dirOf/readOptionalFile) lives in
//	the sibling toml_atomic_write.go, split out per R-14.117/Art.10.3
//	(this ticket's R-14 CR fix pushed the combined file over the 300-line
//	cap while fixing blocking fix 2's Windows path-separator bug).
//
// SPORT: runtime/toml-edit-engine (ADD, placeholder per T-8 sport_updates).

import (
	"strings"
)

// lineScan is the per-document result of scanLines: for every physical
// line, its classification and (for a key=value line) its fully resolved
// dotted path.
type lineScan struct {
	// keyLineOf maps a fully-resolved dotted path to the physical line
	// index that assigns it. Only the FIRST assignment survives (matching
	// TOML's own "duplicate key is an error" rule; this editor is not the
	// place to detect that, so ties resolve to first-write-wins, which is
	// also the intuitive "the value your TOML parser would use" line to
	// edit if a document is somehow malformed).
	keyLineOf map[string]int
	// tableLines maps a table path (e.g. "retrieval.fusion") to the
	// half-open [start,end) line-index range of its body — everything
	// after its header line up to (not including) the next header or
	// EOF. tableLines[""] is the implicit top-level table, [0, firstHeaderLine).
	tableLines map[string][2]int
}

// scanLines walks lines once, tracking the current table context and
// recording every key=value line's resolved dotted path and every table's
// body range.
func scanLines(lines []string) *lineScan {
	scan := &lineScan{keyLineOf: map[string]int{}, tableLines: map[string][2]int{}}
	current := ""
	blockStart := 0
	for i, line := range lines {
		if path, ok := parseTableHeader(line); ok {
			scan.tableLines[current] = [2]int{blockStart, i}
			current = path
			blockStart = i + 1
			continue
		}
		if key, ok := parseKeyValueLine(line); ok {
			dotted := key
			if current != "" {
				dotted = current + "." + key
			}
			if _, exists := scan.keyLineOf[dotted]; !exists {
				scan.keyLineOf[dotted] = i
			}
		}
	}
	scan.tableLines[current] = [2]int{blockStart, len(lines)}
	return scan
}

// findKey reports the physical line index assigning dotted, if any.
func (s *lineScan) findKey(dotted string) (int, bool) {
	i, ok := s.keyLineOf[dotted]
	return i, ok
}

// tableBlockEnd reports the line index just past tablePath's last line
// (i.e. where a new key should be inserted to land inside that table),
// if tablePath's header has been seen.
func (s *lineScan) tableBlockEnd(tablePath string) (int, bool) {
	r, ok := s.tableLines[tablePath]
	if !ok {
		return 0, false
	}
	return r[1], true
}

// parseTableHeader reports the dotted path named by a `[a.b.c]` header
// line, or ok=false for anything else (including `[[array.of.tables]]`,
// deliberately excluded so this editor never treats an array-table entry
// as a settable scalar table context).
func parseTableHeader(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "[") || !strings.HasSuffix(trimmed, "]") {
		return "", false
	}
	if strings.HasPrefix(trimmed, "[[") {
		return "", false
	}
	inner := strings.TrimSpace(trimmed[1 : len(trimmed)-1])
	if inner == "" {
		return "", false
	}
	for _, seg := range strings.Split(inner, ".") {
		if !bareKeyPattern.MatchString(strings.TrimSpace(seg)) {
			return "", false // quoted/complex header segment: not matched by this editor
		}
	}
	return inner, true
}

// parseKeyValueLine reports the bare key text (possibly itself
// dot-separated, e.g. "fusion.k = 80" inside a `[retrieval]` table) of a
// `key = value` assignment line, or ok=false for anything this simple
// scanner does not confidently classify (comments, blank lines, quoted
// keys, continuation lines).
func parseKeyValueLine(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return "", false
	}
	eq := findTopLevelEquals(line)
	if eq == -1 {
		return "", false
	}
	keyPart := strings.TrimSpace(line[:eq])
	if keyPart == "" {
		return "", false
	}
	segs := strings.Split(keyPart, ".")
	normalized := make([]string, len(segs))
	for i, seg := range segs {
		s := strings.TrimSpace(seg)
		if !bareKeyPattern.MatchString(s) {
			return "", false
		}
		normalized[i] = s
	}
	return strings.Join(normalized, "."), true
}

// findTopLevelEquals returns the byte index of the first '=' in line that
// is not inside a single- or double-quoted string, or -1 if none exists.
func findTopLevelEquals(line string) int {
	inSingle, inDouble := false, false
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle && (!inDouble || !isEscapedQuote(line, i)) {
				inDouble = !inDouble
			}
		case '=':
			if !inSingle && !inDouble {
				return i
			}
		}
	}
	return -1
}

// isEscapedQuote reports whether the '"' at index i in s is escaped by
// an odd-length run of backslashes immediately preceding it — TOML's
// basic-string escape rule is that `\\` is one escaped backslash, so an
// EVEN run of backslashes before a `"` leaves that quote un-escaped
// (closes the string) and an ODD run leaves exactly one backslash
// escaping the quote itself (string stays open).
//
// R-14 CR FINDING (P1-E03-W1-S05-T8, nit 5): the previous check looked
// at only the single byte immediately before the quote, so a value
// ending in an even number of backslashes then a quote — a plausible
// Windows path such as `"C:\\"` — had its closing quote misread as
// escaped (still "inside" the string), because that lone preceding byte
// is itself a backslash even though the full run is escaped-backslash
// pairs, not an escaped quote.
func isEscapedQuote(s string, i int) bool {
	n := 0
	for j := i - 1; j >= 0 && s[j] == '\\'; j-- {
		n++
	}
	return n%2 == 1
}

// findValueSpan reports [start, commentStart) for the value portion of
// rest (everything after "="): start is the first non-space byte,
// commentStart is the index of a top-level (outside-quotes) '#' if a
// trailing comment is present, or -1 if the value runs to end-of-line.
func findValueSpan(rest string) (start, commentStart int) {
	start = 0
	for start < len(rest) && (rest[start] == ' ' || rest[start] == '\t') {
		start++
	}
	inSingle, inDouble := false, false
	for i := start; i < len(rest); i++ {
		switch rest[i] {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			// Same backslash-run-parity escape rule as
			// findTopLevelEquals/isEscapedQuote (R-14 CR nit 5) — a value
			// ending in an escaped backslash then a quote (e.g. a Windows
			// path `"C:\\"`) must close the string on that quote, not stay
			// "inside" it because the immediately preceding byte happens
			// to be a backslash.
			if !inSingle && (!inDouble || !isEscapedQuote(rest, i)) {
				inDouble = !inDouble
			}
		case '#':
			if !inSingle && !inDouble {
				return start, i
			}
		}
	}
	return start, -1
}
