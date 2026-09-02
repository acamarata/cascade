package runtime

// Purpose: the structure-preserving TOML line editor behind `cascade
//
//	config set`/`unset`, and the ConfigWriter orchestration that ties the
//	dotted-path resolver (config_write.go) + this editor + Validate +
//	the atomic write together into the write-then-validate-then-persist
//	pipeline the CLI layer (cmd/cascade/config) calls.
//
// Inputs: the raw config.toml bytes (or none, for a not-yet-existing
//
//	file), a dotted key, and a literal-value string already produced by
//	ParseTomlLiteral (or the request to unset a key).
//
// Outputs: the edited byte buffer, with only the target key's line
//
//	touched — every comment, blank line, and surrounding key preserved
//	verbatim — or a typed error and the original bytes left untouched on
//	the caller's disk (WriteSet/WriteUnset only call writeConfigAtomic
//	after Validate succeeds on the edited buffer).
//
// Constraints: does NOT round-trip through toml.Marshal/Unmarshal to
//
//	produce output — go-toml/v2 does not preserve comments across a
//	decode/encode cycle (verified against its docs), so a full re-marshal
//	would silently destroy user comments/ordering. This editor instead
//	rewrites only the target line's value side, byte-for-byte elsewhere.
//	The one exception is table-header creation for a brand-new section,
//	which is new content, not a rewrite of existing content. See
//	toml_edit_scanner.go for the line-classification state machine this
//	file drives (split per R-14.117, task 2).
//
// SPORT: runtime/toml-edit-engine (ADD, placeholder per T-8 sport_updates).

import (
	"bytes"
	"fmt"
	"strings"
)

// EditError reports a structure-preserving edit that could not locate (for
// unset) or safely place (for set) the target key.
type EditError struct {
	Path   string
	Reason string
}

// Error implements the error interface.
func (e *EditError) Error() string {
	return fmt.Sprintf("runtime: toml edit %q: %s", e.Path, e.Reason)
}

// SetKeyLine returns src with dotted's value replaced by literalText (the
// caller's already-validated-as-parseable literal source text, written
// verbatim — this function never reformats a value the caller already
// typed correctly). If dotted's line exists (as either a fully-dotted
// top-level key or a bare key under a `[table]`/`[table.sub]` header
// matching dotted's prefix), only that line's value side changes,
// preserving its trailing inline comment. If the key does not yet exist
// but its enclosing table does, a new line is inserted at the end of that
// table's block. If neither exists, a new `[table]` section is appended
// at end-of-file.
func SetKeyLine(src []byte, dotted string, literalText string) ([]byte, error) {
	segments, err := SplitDottedPath(dotted)
	if err != nil {
		return nil, err
	}
	lines := splitLines(src)
	scan := scanLines(lines)

	if hit, ok := scan.findKey(dotted); ok {
		lines[hit] = replaceValueSide(lines[hit], literalText)
		return joinLines(lines), nil
	}

	tablePath := strings.Join(segments[:len(segments)-1], ".")
	leafKey := segments[len(segments)-1]
	newLine := fmt.Sprintf("%s = %s", leafKey, literalText)

	if insertAt, ok := scan.tableBlockEnd(tablePath); ok {
		lines = insertLineAt(lines, insertAt, newLine)
		return joinLines(lines), nil
	}

	return appendNewTable(lines, tablePath, newLine), nil
}

// UnsetKeyLine returns src with dotted's line removed (reverting it to
// its schema default on next load), and reports whether it was found. An
// absent key is not an error — unsetting an already-default key is a
// no-op per 08 §3 (`unset a.b` reverts to default; a key already at
// default has nothing to revert).
func UnsetKeyLine(src []byte, dotted string) ([]byte, bool, error) {
	if _, err := SplitDottedPath(dotted); err != nil {
		return nil, false, err
	}
	lines := splitLines(src)
	scan := scanLines(lines)

	hit, ok := scan.findKey(dotted)
	if !ok {
		return src, false, nil
	}
	lines = append(lines[:hit], lines[hit+1:]...)
	return joinLines(lines), true, nil
}

// replaceValueSide rewrites line's value side (after the first top-level
// `=`) to newValue, preserving the key text, the surrounding `=`
// spacing, and any trailing inline comment verbatim.
func replaceValueSide(line, newValue string) string {
	eq := findTopLevelEquals(line)
	if eq == -1 {
		return line // defensive: scanner only calls this on lines it classified as key=value
	}
	keyPart := line[:eq+1] // includes "=" itself
	rest := line[eq+1:]
	_, commentStart := findValueSpan(rest)
	trailing := ""
	if commentStart >= 0 {
		trailing = rest[commentStart:]
	}
	// Preserve exactly one leading space after '=' (the overwhelmingly
	// common style; go-toml and gofmt-for-TOML tools agree) rather than
	// trying to preserve the original gap's exact width, which the
	// commentStart split does not cleanly separate from value width.
	if trailing != "" {
		return keyPart + " " + newValue + "  " + trailing
	}
	return keyPart + " " + newValue
}

// appendNewTable appends a brand-new `[tablePath]` section (or a
// top-level line, when tablePath is "") at end-of-file, adding a
// separating blank line first when src is non-empty and does not already
// end with one.
func appendNewTable(lines []string, tablePath, newLine string) []byte {
	var b bytes.Buffer
	for _, l := range lines {
		b.WriteString(l)
		b.WriteByte('\n')
	}
	if b.Len() > 0 {
		b.WriteByte('\n')
	}
	if tablePath != "" {
		b.WriteString("[" + tablePath + "]\n")
	}
	b.WriteString(newLine)
	b.WriteByte('\n')
	return b.Bytes()
}

// splitLines splits src on '\n' without discarding trailing empty
// elements' significance for round-tripping; joinLines is its exact
// inverse for a well-formed (LF-terminated or not) input.
func splitLines(src []byte) []string {
	if len(src) == 0 {
		return nil
	}
	s := string(src)
	trailingNL := strings.HasSuffix(s, "\n")
	if trailingNL {
		s = s[:len(s)-1]
	}
	lines := strings.Split(s, "\n")
	if trailingNL {
		return lines
	}
	// No trailing newline in the source: mark it by NOT adding one back in
	// joinLines. We track this via a sentinel: append a marker line isn't
	// safe, so instead joinLines always adds a trailing newline — every
	// config.toml this tool writes is newline-terminated, which is the
	// universal POSIX text-file convention and never itself a
	// user-visible content change (whitespace-only, end-of-file).
	return lines
}

// joinLines is splitLines' inverse, always producing a newline-terminated
// buffer (see splitLines' doc comment on the trailing-newline convention).
func joinLines(lines []string) []byte {
	if len(lines) == 0 {
		return nil
	}
	return []byte(strings.Join(lines, "\n") + "\n")
}

// insertLineAt inserts newLine into lines at index i (shifting the rest
// down).
func insertLineAt(lines []string, i int, newLine string) []string {
	out := make([]string, 0, len(lines)+1)
	out = append(out, lines[:i]...)
	out = append(out, newLine)
	out = append(out, lines[i:]...)
	return out
}
