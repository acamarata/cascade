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
//	settable table context by this editor).
//
// SPORT: runtime/toml-edit-engine (ADD, placeholder per T-8 sport_updates).

import (
	"os"
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
			if !inDouble || i == 0 || line[i-1] != '\\' {
				if !inSingle {
					inDouble = !inDouble
				}
			}
		case '=':
			if !inSingle && !inDouble {
				return i
			}
		}
	}
	return -1
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
			if !inSingle {
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

// readOptionalFile reads path, tolerating a not-yet-existing file as an
// empty document (matching config_load.go's Load: `cascade config set` on
// a fresh CASCADE_HOME with no config.toml yet creates one rather than
// erroring).
func readOptionalFile(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		return data, nil
	}
	if os.IsNotExist(err) {
		return nil, nil
	}
	return nil, err
}

// writeBytesAtomic writes data to path via the same temp-file-in-same-dir
// + rename pattern as config_load.go's writeConfigAtomic (R-14.106
// precedent this ticket's contract requires reusing rather than
// reimplementing a second atomic-write primitive): a crash mid-write
// leaves either the untouched original or nothing at the temp path, never
// a truncated config.toml.
func writeBytesAtomic(path string, data []byte) error {
	dir := dirOf(path)
	if dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	tmp, err := os.CreateTemp(dir, ".config-*.toml.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

// dirOf returns the directory portion of path without importing
// path/filepath a second time in this file's import list beyond what it
// already needs; kept trivially simple (no ".." handling required — every
// caller passes an already-resolved PathProvider path).
func dirOf(path string) string {
	i := strings.LastIndexByte(path, '/')
	if i < 0 {
		return ""
	}
	return path[:i]
}
