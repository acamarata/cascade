// Purpose: parse one "// SPORT:" marker line's text into one or more raw
//
//	entity declarations. This is the schema derived from scanning the real
//	tree (doc.go) made executable.
//
// Inputs: the file, 1-based line number, and marker text (everything after
//
//	"SPORT:", NOT including the leading "// " comment marker).
//
// Outputs: one Site-shaped ParsedEntity per top-level segment, or a
//
//	ParseError when a segment carries no extractable name at all.
//
// Constraints: never silently drops a segment. A segment that yields an
//
//	empty name after every extraction rule is a ParseError, returned to
//	the caller rather than skipped — a silently-skipped line is an entity
//	that vanishes from the registry (the owner's own framing). A segment
//	that DOES yield a name but no recognized status verb is NOT an error:
//	it becomes an Entity with StatusUnspecified (see doc.go for the line
//	between the two).
//
// SPORT: internal.inventory.sport.ParseMarkerLine/ADDED.

package sport

import (
	"fmt"
	"regexp"
	"strings"
)

// ParsedEntity is one segment's extraction result, before dedup.
type ParsedEntity struct {
	Name   string
	Status string
	Ticket string
	Raw    string
}

// ParseError reports a marker segment with no extractable name.
type ParseError struct {
	File string
	Line int
	Text string
}

func (e ParseError) Error() string {
	return fmt.Sprintf("%s:%d: SPORT marker has no extractable entity name: %q", e.File, e.Line, e.Text)
}

// statusAliases maps every status-verb spelling found in the live tree
// (doc.go's distribution) to one of four normalized statuses. CHG is an
// abbreviation for CHANGE found once (cmd/cascade/node.go); REMOVE(D) and
// DEPRECATE(D) do not occur yet but are recognized since this taxonomy is
// meant to outlive the current tree contents.
var statusAliases = map[string]string{
	"ADD": "ADD", "ADDED": "ADD",
	"CHANGE": "CHANGE", "CHANGED": "CHANGE", "CHG": "CHANGE",
	"REMOVE": "REMOVE", "REMOVED": "REMOVE",
	"DEPRECATE": "DEPRECATE", "DEPRECATED": "DEPRECATE",
}

var statusWordPattern = regexp.MustCompile(`\b(ADDED|ADD|CHANGED|CHANGE|CHG|REMOVED|REMOVE|DEPRECATED|DEPRECATE)\b`)

// ticketPattern matches this repo's two ticket-id shapes: the full
// Phase-Epic-Wave-Sprint-Ticket form (P1-E17-W4-S36-T4) and the short
// per-file form (T-3).
var ticketPattern = regexp.MustCompile(`\bP1-E\d+-W\d+-S\d+-T\d+\b|\bT-\d+\b`)

// separatorPattern finds the first of this tree's other name/detail
// boundaries: an opening paren or bracket, a colon-space, an em dash, or a
// middle dot, each optionally preceded by whitespace.
var separatorPattern = regexp.MustCompile(`\s*[(\[]|:\s|\s—\s|\s·\s`)

// ParseMarkerLine splits text on top-level commas (respecting parens and
// brackets, per doc.go's three-entity example) and extracts one
// ParsedEntity per segment.
func ParseMarkerLine(file string, line int, text string) ([]ParsedEntity, []ParseError) {
	var entities []ParsedEntity
	var errs []ParseError
	prevName := ""
	for _, seg := range splitTopLevelSegments(text) {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		name, status := extractNameAndStatus(seg)
		if name == "" && prevName != "" {
			// A later top-level segment with a status verb but no name
			// of its own (e.g. "X/ADDED (t1), CHANGED (t2 continues on
			// the next comment line)") restates the SAME entity's later
			// status, not a second entity — see doc.go's worked example
			// (providers/sqlite/lock_test.go). Inferring rather than
			// failing here keeps this from vanishing as a false
			// "malformed" hit on a real, common shorthand.
			name = prevName
		}
		if name == "" {
			errs = append(errs, ParseError{File: file, Line: line, Text: seg})
			continue
		}
		prevName = name
		entities = append(entities, ParsedEntity{
			Name:   name,
			Status: status,
			Ticket: ticketPattern.FindString(seg),
			Raw:    seg,
		})
	}
	return entities, errs
}

// hasStatusWord reports whether any segment in segs carries a recognized
// status verb — used to decide whether comma-splitting segs at all is
// safe (see splitTopLevelSegments's doc comment).
func hasStatusWord(segs []string) bool {
	for _, s := range segs {
		if statusWordPattern.MatchString(s) {
			return true
		}
	}
	return false
}
