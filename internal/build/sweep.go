// Package build (this file) holds the personal-identifier sweep from
// P1-E01-W1-S01-T5 (PRI hard rule 3: "no personal account/lane identifiers
// in tracked files or commit messages ... identifier sweep before push").
//
// # Where the deny-pattern list lives, and why it is never here
//
// The actual deny patterns (real account/lane identifiers) MUST NOT live in
// any tracked file — writing them into sweep.go or sweep_test.go to check
// against would itself violate PRI hard rule 3 on this public repo. They
// come from exactly two sources, resolved by LoadPatterns:
//  1. an UNTRACKED local file on a dev machine, named by
//     CASCADE_IDENTIFIER_PATTERNS_FILE. The one canonical file is
//     ~/.claude/cascade-hygiene/identifier-patterns.txt, outside every
//     clone; the pre-push hook falls back to it and the CI secret is
//     refreshed from it. Policy: a pattern file inside the repo tree (for
//     example under .claude/hygiene/) must never be used as a source;
//  2. a masked CI variable/secret, CASCADE_IDENTIFIER_PATTERNS, whose value
//     is the same format inline instead of a file path.
//
// # Testdata is scanned, and output never echoes a match
//
// Unlike the secret scan and the other gates that use SweepSkipsPath, this
// sweep scans testdata: captured fixtures are where a private host or
// account id is most likely to land. Only internal/build/testdata, which
// holds the seeded-violation fixtures, is exempt (IdentifierSweepSkipsPath).
// A violation is reported as file:line plus the 0-based index of the
// pattern that matched, never the pattern text or the matched line, so the
// gate's own log (CI output is public) cannot leak what it guards.
//
// One pattern per line, blank lines and "#"-prefixed comments ignored,
// each line a Go RE2 regular expression (regexp.Compile). Neither source
// existing (or existing but producing zero usable patterns) is a hard
// error, not a skip — LoadPatterns always fails closed.
//
// # What this gate does and does not catch
//
// It is a literal RE2 regex scan over each tracked file's UTF-8 content
// and each commit message in a range, line by line. It DOES catch: any
// exact or pattern-matched substring the loaded deny-set targets,
// including multiple independent hits per file. It does NOT catch:
// identifiers absent from the loaded pattern set (the sweep is only as
// good as its pattern source — this gate ships with zero hardcoded
// patterns of its own, by design: an earlier draft hardcoded generic
// "structural" patterns for /Users/<name> paths and free-mail addresses
// as always-on defense in depth, and probing it against the real tree
// immediately false-positived on docs/cli-reference/config.md's
// documented example paths (`/Users/me/.cascade/...`) — legitimate
// tracked content, not a leak. A hardcoded pattern with no allowlist
// mechanism is worse than no hardcoded pattern: it either has to special-
// case real content it doesn't actually understand, or it blocks every
// push once wired in. The external pattern source is expected to encode
// this repo's real deny-list precisely, including any repo-specific
// exemptions its author chooses not to flag); obfuscated/base64/rot13/
// whitespace-mangled or image/EXIF-embedded identifiers; matches inside
// binary files (their raw bytes are scanned, but compressed or encoded
// content is not decoded, so a gzip-wrapped capture passes); history
// outside the scanned commit range or tree (a prior push that already
// leaked an identifier is not retroactively caught). A green run is proof
// the loaded patterns found nothing — never proof no identifier exists.
package build

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// Environment variable names LoadPatterns consults, in priority order: the
// local-file pointer first (dev machines), then the inline CI variable.
const (
	IdentifierPatternsFileEnvVar = "CASCADE_IDENTIFIER_PATTERNS_FILE"
	IdentifierPatternsEnvVar     = "CASCADE_IDENTIFIER_PATTERNS"
)

// The two hygiene gates' commit-range variables, named here so the gate
// that reads one can say which it wanted when somebody sets the other.
//
// Two near-identical names is a hazard by itself. What made it a real one
// is that setting the wrong name made the identifier sweep scan tracked
// files ONLY and report SUCCESS — a pre-push check failing open, and
// silently, for whoever confused them.
const (
	// SweepRangeEnvVar is the range the identifier sweep scans commit
	// messages over.
	SweepRangeEnvVar = "CASCADE_HYGIENE_SWEEP_RANGE"
	// CommitRangeEnvVar is the CONVENTIONAL-COMMIT gate's variable, read
	// by the sweep only to recognise it having been set by mistake.
	CommitRangeEnvVar = "CASCADE_HYGIENE_COMMIT_RANGE"
)

// SweepViolation is one pattern hit: where it was found (a tracked file's
// repo-relative path, or "commit-message[N]") and on which line (1-based;
// 0 for a match that spans how SweepContent joins lines, which currently
// never happens since it matches per line).
//
// Pattern and Snippet stay in memory for FilterAllowed and callers that
// need them; FormatSweepViolations never prints either. PatternIndex is the
// 0-based index of the matching pattern among the loaded patterns.
type SweepViolation struct {
	Source       string
	Line         int
	Pattern      string
	PatternIndex int
	Snippet      string
}

// ParsePatterns decodes the one-pattern-per-line format both pattern
// sources use. An empty result (nothing but blanks/comments, or empty
// input) is an error — fail closed, per this file's package doc. A pattern
// that does not compile is reported by its 1-based line number only: the
// pattern text and the regexp error (which quotes it) never reach the log.
func ParsePatterns(raw string) ([]*regexp.Regexp, error) {
	var pats []*regexp.Regexp
	for i, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, allowPrefix) {
			continue
		}
		re, err := regexp.Compile(line)
		if err != nil {
			return nil, fmt.Errorf("identifier sweep: invalid pattern at line %d", i+1)
		}
		pats = append(pats, re)
	}
	if len(pats) == 0 {
		return nil, errors.New("identifier sweep: pattern source produced zero patterns (fail closed)")
	}
	return pats, nil
}

// SweepContent scans content line by line against every pattern, returning
// one SweepViolation per (line, pattern) hit, in line order.
func SweepContent(patterns []*regexp.Regexp, source string, content []byte) []SweepViolation {
	var out []SweepViolation
	for i, line := range strings.Split(string(content), "\n") {
		for j, re := range patterns {
			if re.FindStringIndex(line) != nil {
				out = append(out, SweepViolation{Source: source, Line: i + 1, Pattern: re.String(),
					PatternIndex: j, Snippet: strings.TrimSpace(line)})
			}
		}
	}
	return out
}

// SweepCommitMessages scans every message in messages (index-labeled,
// since a commit range at this layer is just message text — callers that
// have real SHAs, e.g. a live gate over CommitRef, can relabel Source
// themselves) against patterns.
func SweepCommitMessages(patterns []*regexp.Regexp, messages []string) []SweepViolation {
	var out []SweepViolation
	for i, msg := range messages {
		out = append(out, SweepContent(patterns, fmt.Sprintf("commit-message[%d]", i), []byte(msg))...)
	}
	return out
}

// ListTrackedFiles returns every path `git ls-files` reports for root,
// repo-relative with forward slashes — exactly PRI hard rule 3's "tracked
// files" scope (never an untracked/.gitignored file, which could
// legitimately hold the identifier-pattern source itself).
func ListTrackedFiles(root string) ([]string, error) {
	out, err := exec.Command("git", "-C", root, "ls-files", "-z").Output()
	if err != nil {
		return nil, fmt.Errorf("identifier sweep: git ls-files: %w", err)
	}
	var files []string
	for _, f := range strings.Split(strings.TrimRight(string(out), "\x00"), "\x00") {
		if f != "" {
			files = append(files, f)
		}
	}
	return files, nil
}

// SweepSkipsPath reports whether rel is fixture data the sweep must not
// scan. Seeded-violation fixtures exist precisely to CONTAIN the strings
// the gate hunts for, so scanning them makes the gate permanently red on
// its own test data and blocks every push — which is exactly what
// happened before this exclusion existed. The match is on whole path
// segments, never a substring: an unanchored "testdata" check is how the
// lint wall came to exempt every path merely containing that word.
func SweepSkipsPath(rel string) bool {
	for _, seg := range strings.Split(filepath.ToSlash(rel), "/") {
		if seg == "testdata" {
			return true
		}
	}
	return false
}

// identifierSweepExempt is the one directory the identifier sweep skips:
// the seeded-violation fixtures that exist to contain what it hunts for.
var identifierSweepExempt = []string{"internal", "build", "testdata"}

// IdentifierSweepSkipsPath reports whether the identifier sweep skips rel.
// It is true only for a path below internal/build/testdata/, matched on
// whole leading segments of the repo-relative path. Every other testdata
// directory is scanned, unlike SweepSkipsPath, which the secret scan and
// the other gates keep using.
func IdentifierSweepSkipsPath(rel string) bool {
	segs := strings.Split(strings.TrimPrefix(filepath.ToSlash(rel), "./"), "/")
	if len(segs) <= len(identifierSweepExempt) {
		return false
	}
	for i, want := range identifierSweepExempt {
		if segs[i] != want {
			return false
		}
	}
	return true
}

// SweepFiles reads each of relFiles under root and scans its content
// against patterns. A read error is returned as an error, never silently
// skipped — fail closed, per this file's package doc. Binary files are
// scanned as raw bytes and never error. Testdata is scanned;
// only internal/build/testdata is excluded (IdentifierSweepSkipsPath).
func SweepFiles(patterns []*regexp.Regexp, root string, relFiles []string) ([]SweepViolation, error) {
	var out []SweepViolation
	for _, rel := range relFiles {
		if IdentifierSweepSkipsPath(rel) {
			continue
		}
		data, err := os.ReadFile(root + string(os.PathSeparator) + rel)
		if err != nil {
			return nil, fmt.Errorf("identifier sweep: reading %s: %w", rel, err)
		}
		out = append(out, SweepContent(patterns, rel, data)...)
	}
	return out, nil
}

// FormatSweepViolations renders violations as one line each, for gate
// error/log output: file:line and the pattern index only. Printing the
// pattern or the matched line would publish the private identifier in the
// very log meant to keep it out of public view.
func FormatSweepViolations(violations []SweepViolation) string {
	var b strings.Builder
	for _, v := range violations {
		fmt.Fprintf(&b, "  - %s:%d matches pattern #%d\n", v.Source, v.Line, v.PatternIndex)
	}
	return strings.TrimRight(b.String(), "\n")
}
