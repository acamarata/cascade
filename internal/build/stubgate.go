// Package build (this file) implements the Article-1 anti-stub gate from
// P1-E01-W1-S01-T8 (12-QUALITY-CONSTITUTION.md Art.1.2): non-test Go files
// under cmd/, internal/, pkg/, providers/, plugins/ may not contain a
// panic call whose string argument reads as an unfinished-work marker, an
// unfinished-work comment DIRECTIVE in the three-letter/four-letter Go
// convention (the marker word starts the comment body itself — not any
// sentence that merely mentions the word), a Mock-/Fake-/Noop-prefixed
// type declaration, or a two-nil return trailed by a specific marker
// comment. A violation is CI-red unless the offending line (or the line
// immediately above it) carries a syntactically valid
// `// CASCADE-ALLOW: <ticket-id> <reason>` escape.
//
// # Why this is AST-precise, not a raw substring scan (a real false
// positive, found by probing this exact gate against the real tree)
//
// An early draft matched the denied phrases against every line's raw text,
// including inside ordinary "//" comments. Probed against the real tree,
// it immediately false-positived on two legitimate files that DISCUSS the
// rule rather than violate it: this file's own package doc (which must
// name the denied vocabulary to define it) and
// plugins/examples/example-builtin/plugin.go's doc comment ("a REAL
// implementation — no panic with an unfinished-work string, no
// Noop-prefixed types"). A gate that cannot describe its own rule without
// tripping itself, or that blocks an unrelated file for talking ABOUT the
// rule, is not a durable gate. The fix: the phrase-style markers ("not
// implemented", "unimplemented") are matched only against actual Go STRING
// LITERALS via AST (go/parser without ast.ParseComments does not even
// retain comment text as parseable nodes, so a plain "//" comment
// mentioning the phrase is invisible to this scan by construction) — this
// catches `panic("not implemented")` and `errors.New("unimplemented")`,
// which is what Art.1.2 actually targets, without matching prose. The
// three unfinished-work directive markers (see stubgateDirectivePattern
// below for the exact three) stay a raw-line scan — comments are exactly
// where those legitimately live — but are anchored to the Go directive
// convention: the marker word must START the comment body, so a sentence
// that merely uses the word mid-clause does not fire.
//
// # What "AND an open ticket" means here, honestly
//
// Art.1.2's escape clause requires "a // CASCADE-ALLOW: <ticket-id> <reason>
// comment AND an open ticket". This repo's ticket state
// (.claude/planning/p1/phase/**) is gitignored per PRI hard rule 3 ("no
// personal account/lane identifiers" — the whole .claude/ tree, including
// ticket YAML, never ships in the public tree) — so a CI job running against
// a checkout of this PUBLIC repo has no ticket database to query at all.
// This gate therefore enforces only what a public CI checkout CAN verify:
// the comment's syntactic shape (a ticket-id matching this plan's id
// grammar, plus a non-empty reason). It does not and cannot verify the
// referenced ticket is actually open — that half of Art.1.2 is a process
// control enforced by the planning corpus and ticket review (CR-B), not by
// this gate. Claiming otherwise here would be exactly the kind of gate
// that looks correct and enforces nothing, which this ticket exists to
// avoid, so the gap is named instead of papered over.
//
// This file holds pure scanning logic with zero dependency on "testing" —
// the walking/fixture-materialization glue lives in stubgate_test.go, same
// separation as sweep.go/sweep_test.go.
package build

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// stubgateTicketIDPattern is this plan's contract-id grammar (e.g.
// "P1-E01-W1-S01-T8"), used to validate the ticket-id half of a
// CASCADE-ALLOW escape comment.
var stubgateTicketIDPattern = regexp.MustCompile(`^P\d+-[A-Z]+\d*-W\d+-S\d+-T\d+$`)

// stubgateAllowPattern matches a CASCADE-ALLOW escape comment and captures
// the ticket-id token and the remainder as the reason.
var stubgateAllowPattern = regexp.MustCompile(`CASCADE-ALLOW:\s*(\S+)\s+(.+)$`)

// stubgateDirectivePattern matches a "//"-comment line whose content
// STARTS with one of the three denied directive words (optionally
// followed by ":" or "(" per common Go convention, e.g. "// TODO(name):").
// Anchoring to the start of the comment body is what distinguishes a real
// leftover marker from a sentence that merely uses the word.
var stubgateDirectivePattern = regexp.MustCompile(`^\s*//+\s*(TODO|FIXME|XXX)\b`)

// stubgateStringPhrases are the phrase-style Art.1.2 markers, matched only
// against Go string-literal VALUES (via AST below), never raw comment
// text.
var stubgateStringPhrases = []string{"not implemented", "unimplemented"}

// stubgateTypePrefixes are the denied type-name prefixes (Art.1.2:
// "Mock*/Fake*/Noop* type names").
var stubgateTypePrefixes = []string{"Mock", "Fake", "Noop"}

// stubgateNilNilReturn and stubgateMarkerWord are split into two constants,
// deliberately never written adjacent to each other anywhere else in this
// file's own source (including comments), so this file does not trip its
// own two-part co-occurrence check below.
const (
	stubgateNilNilReturn = "return nil, nil"
	stubgateMarkerWord   = "place" + "holder"
)

// StubViolation is one Article-1 marker found in a shipped file, not
// suppressed by a CASCADE-ALLOW escape.
type StubViolation struct {
	File   string
	Line   int
	Marker string
}

// stubgateAllowedLines returns the set of 1-based line numbers in src that
// carry a syntactically valid CASCADE-ALLOW escape (ticket-id matches the
// plan's grammar, reason non-empty).
func stubgateAllowedLines(src []byte) map[int]bool {
	allowed := make(map[int]bool)
	for i, line := range strings.Split(string(src), "\n") {
		m := stubgateAllowPattern.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		ticketID, reason := m[1], strings.TrimSpace(m[2])
		if stubgateTicketIDPattern.MatchString(ticketID) && reason != "" {
			allowed[i+1] = true
		}
	}
	return allowed
}

// stubgateSuppressed reports whether line (1-based) is covered by an escape
// on the same line or the line immediately above it.
func stubgateSuppressed(allowed map[int]bool, line int) bool {
	return allowed[line] || allowed[line-1]
}

// StubgateScanFile scans one non-test .go file for Article-1 markers:
// directive-style TODO/FIXME/XXX comments and the two-nil-plus-marker
// return (raw line text), then string-literal phrase markers and denied
// type-name prefixes (AST), dropping any finding covered by a
// CASCADE-ALLOW escape. Exported so the _test.go walker in this package
// can call it directly.
func StubgateScanFile(path string) ([]StubViolation, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	allowed := stubgateAllowedLines(data)
	out := stubgateScanText(path, data, allowed)

	fset := token.NewFileSet()
	file, perr := parser.ParseFile(fset, path, data, 0)
	if perr != nil {
		// A file that fails to parse is a build error, not this gate's
		// concern to diagnose further; skip the AST-only checks for it.
		return out, nil
	}
	out = append(out, stubgateScanAST(path, fset, file, allowed)...)
	return out, nil
}

// stubgateScanText scans one file's raw text for the directive-style
// TODO/FIXME/XXX markers and the nil-nil-plus-placeholder return.
func stubgateScanText(path string, data []byte, allowed map[int]bool) []StubViolation {
	var out []StubViolation
	for i, line := range strings.Split(string(data), "\n") {
		lineNo := i + 1
		if stubgateSuppressed(allowed, lineNo) {
			continue
		}
		if m := stubgateDirectivePattern.FindStringSubmatch(line); m != nil {
			out = append(out, StubViolation{File: path, Line: lineNo, Marker: m[1] + " directive comment"})
		}
		if strings.Contains(line, stubgateNilNilReturn) && strings.Contains(line, stubgateMarkerWord) {
			out = append(out, StubViolation{File: path, Line: lineNo, Marker: "nil-nil return with a placeholder marker comment"})
		}
	}
	return out
}

// stubgateScanAST scans one parsed file's AST for denied string-literal
// phrases and denied type-name prefixes.
func stubgateScanAST(path string, fset *token.FileSet, file *ast.File, allowed map[int]bool) []StubViolation {
	var out []StubViolation
	ast.Inspect(file, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.BasicLit:
			if node.Kind != token.STRING {
				return true
			}
			val, uerr := strconv.Unquote(node.Value)
			if uerr != nil {
				return true
			}
			lower := strings.ToLower(val)
			for _, phrase := range stubgateStringPhrases {
				if strings.Contains(lower, phrase) {
					lineNo := fset.Position(node.Pos()).Line
					if !stubgateSuppressed(allowed, lineNo) {
						out = append(out, StubViolation{File: path, Line: lineNo, Marker: "string literal contains " + strconv.Quote(phrase)})
					}
				}
			}
		case *ast.TypeSpec:
			for _, prefix := range stubgateTypePrefixes {
				if strings.HasPrefix(node.Name.Name, prefix) {
					lineNo := fset.Position(node.Pos()).Line
					if !stubgateSuppressed(allowed, lineNo) {
						out = append(out, StubViolation{File: path, Line: lineNo, Marker: "type " + node.Name.Name + " (denied " + prefix + "-prefix)"})
					}
				}
			}
		}
		return true
	})
	return out
}
