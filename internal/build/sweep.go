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
//     CASCADE_IDENTIFIER_PATTERNS_FILE (default
//     .claude/hygiene/identifier-patterns.txt — under the already-gitignored
//     .claude/ tree, PRI hard rule 3);
//  2. a masked CI variable/secret, CASCADE_IDENTIFIER_PATTERNS, whose value
//     is the same format inline instead of a file path.
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
// binary files (a non-UTF8 read is surfaced as a walk error, which fails
// the gate closed rather than silently skipping the file); history
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

// SweepViolation is one pattern hit: where it was found (a tracked file's
// repo-relative path, or "commit-message[N]") and on which line (1-based;
// 0 for a match that spans how SweepContent joins lines, which currently
// never happens since it matches per line).
type SweepViolation struct {
	Source  string
	Line    int
	Pattern string
	Snippet string
}

// ParsePatterns decodes the one-pattern-per-line format both pattern
// sources use. An empty result (nothing but blanks/comments, or empty
// input) is an error — fail closed, per this file's package doc.
func ParsePatterns(raw string) ([]*regexp.Regexp, error) {
	var pats []*regexp.Regexp
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, allowPrefix) {
			continue
		}
		re, err := regexp.Compile(line)
		if err != nil {
			return nil, fmt.Errorf("identifier sweep: invalid pattern %q: %w", line, err)
		}
		pats = append(pats, re)
	}
	if len(pats) == 0 {
		return nil, errors.New("identifier sweep: pattern source produced zero patterns (fail closed)")
	}
	return pats, nil
}

// allowPrefix marks an allow pattern in a pattern source. A line beginning
// with it is compiled as an exemption rather than a denial.
const allowPrefix = "!"

// ParseAllowPatterns decodes the allow ("!"-prefixed) lines of a pattern
// source. Allow patterns exist for the narrow case where a denied string is
// REQUIRED to appear somewhere: the copyright holder's name in LICENSE is
// the motivating example, since a licence that does not name its holder is
// not a licence.
//
// An allow pattern suppresses a hit only when it matches the SAME line that
// tripped the denial. It is deliberately not a file-level exemption. Skipping
// the whole file would stop scanning it for every other pattern, so a leaked
// address or lane identifier buried in that file would sail through. Here the
// file stays fully scanned and only the one justified line is forgiven.
//
// Unlike ParsePatterns, an empty result is NOT an error: having no exemptions
// is the normal, stricter case.
func ParseAllowPatterns(raw string) ([]*regexp.Regexp, error) {
	var out []*regexp.Regexp
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, allowPrefix) {
			continue
		}
		body := strings.TrimSpace(strings.TrimPrefix(line, allowPrefix))
		if body == "" {
			return nil, errors.New("identifier sweep: empty allow pattern (fail closed)")
		}
		re, err := regexp.Compile(body)
		if err != nil {
			return nil, fmt.Errorf("identifier sweep: invalid allow pattern %q: %w", body, err)
		}
		out = append(out, re)
	}
	return out, nil
}

// LoadAllowPatterns resolves allow patterns from the same source LoadPatterns
// uses, so both live in one file and cannot drift apart.
func LoadAllowPatterns() ([]*regexp.Regexp, error) {
	switch {
	case os.Getenv(IdentifierPatternsFileEnvVar) != "":
		data, err := os.ReadFile(os.Getenv(IdentifierPatternsFileEnvVar))
		if err != nil {
			return nil, fmt.Errorf("identifier sweep: reading pattern file: %w (fail closed)", err)
		}
		return ParseAllowPatterns(string(data))
	case os.Getenv(IdentifierPatternsEnvVar) != "":
		return ParseAllowPatterns(os.Getenv(IdentifierPatternsEnvVar))
	default:
		return nil, fmt.Errorf("identifier sweep: no pattern source configured (fail closed)")
	}
}

// FilterAllowed drops violations whose snippet is matched by an allow
// pattern. It reports how many it dropped so a caller can surface the count:
// an exemption that silently swallows hits is how a gate stops being a gate.
func FilterAllowed(violations []SweepViolation, allows []*regexp.Regexp) ([]SweepViolation, int) {
	if len(allows) == 0 {
		return violations, 0
	}
	var kept []SweepViolation
	dropped := 0
	for _, v := range violations {
		exempt := false
		for _, re := range allows {
			if re.MatchString(v.Snippet) {
				exempt = true
				break
			}
		}
		if exempt {
			dropped++
			continue
		}
		kept = append(kept, v)
	}
	return kept, dropped
}

// LoadPatternsFromFile reads and parses the pattern file at path.
func LoadPatternsFromFile(path string) ([]*regexp.Regexp, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("identifier sweep: reading pattern file %s: %w (fail closed)", path, err)
	}
	return ParsePatterns(string(data))
}

// LoadPatternsFromEnv reads and parses the pattern set from env var name.
func LoadPatternsFromEnv(name string) ([]*regexp.Regexp, error) {
	raw, ok := os.LookupEnv(name)
	if !ok {
		return nil, fmt.Errorf("identifier sweep: env var %s not set (fail closed)", name)
	}
	return ParsePatterns(raw)
}

// LoadPatterns resolves the external pattern source per this file's
// package doc (file pointer env var first, then inline CI env var). It
// returns an error — never an empty, silently-skipped result — when
// neither source is configured or the configured one fails to parse.
func LoadPatterns() ([]*regexp.Regexp, error) {
	switch {
	case os.Getenv(IdentifierPatternsFileEnvVar) != "":
		return LoadPatternsFromFile(os.Getenv(IdentifierPatternsFileEnvVar))
	case os.Getenv(IdentifierPatternsEnvVar) != "":
		return LoadPatternsFromEnv(IdentifierPatternsEnvVar)
	default:
		return nil, fmt.Errorf("identifier sweep: no pattern source configured (set %s or %s) (fail closed)",
			IdentifierPatternsFileEnvVar, IdentifierPatternsEnvVar)
	}
}

// SweepContent scans content line by line against every pattern, returning
// one SweepViolation per (line, pattern) hit, in line order.
func SweepContent(patterns []*regexp.Regexp, source string, content []byte) []SweepViolation {
	var out []SweepViolation
	for i, line := range strings.Split(string(content), "\n") {
		for _, re := range patterns {
			if re.FindStringIndex(line) != nil {
				out = append(out, SweepViolation{Source: source, Line: i + 1, Pattern: re.String(), Snippet: strings.TrimSpace(line)})
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

// SweepFiles reads each of relFiles under root and scans its content
// against patterns. A read error (including a non-UTF8/binary file this
// gate cannot meaningfully scan) is returned as an error, never silently
// skipped — fail closed, per this file's package doc. Paths under a
// testdata segment are excluded per SweepSkipsPath.
func SweepFiles(patterns []*regexp.Regexp, root string, relFiles []string) ([]SweepViolation, error) {
	var out []SweepViolation
	for _, rel := range relFiles {
		if SweepSkipsPath(rel) {
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
// error/log output.
func FormatSweepViolations(violations []SweepViolation) string {
	var b strings.Builder
	for _, v := range violations {
		fmt.Fprintf(&b, "  - %s:%d matches %q: %s\n", v.Source, v.Line, v.Pattern, v.Snippet)
	}
	return strings.TrimRight(b.String(), "\n")
}
