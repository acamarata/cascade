// Package build (this file) holds the conventional-commit checker from
// P1-E01-W1-S01-T5 (12-QUALITY-CONSTITUTION.md Art.3 Ticket DoD; ASI/GCI
// git standard "Conventional Commits"; PRI hard rule 5 "conventional
// commits"). It validates a commit range, not just HEAD, and skips merge
// commits — a merge subject ("Merge branch 'x' into y") is never expected
// to parse as type(scope)?: subject.
//
// This file is invoked two ways: (1) as a pure library by commits_test.go,
// exercised against synthetic and seeded-fixture data with no I/O; (2) as
// the live gate via TestConventionalCommitGate_Live, which the pre-push
// hook (.github/hooks/pre-push) and the CI hygiene job both opt into by
// setting CASCADE_HYGIENE_RUN=1 and CASCADE_HYGIENE_COMMIT_RANGE — so a
// bare `go test ./...` run by an unrelated concurrent ticket never trips
// this gate merely because it lacks that range.
package build

import (
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strings"
)

// CommitTypes is the repo-standard conventional-commit type set
// (~/.claude/standards/git.md, matched by this repo's real history: feat,
// fix, chore, refactor, ... are already in use on main). "build" and
// "revert" are included as a superset beyond the documented table — they
// are standard Conventional Commits types and accepting them can never
// block a message the documented table would have allowed.
var CommitTypes = map[string]bool{
	"feat":     true,
	"fix":      true,
	"chore":    true,
	"docs":     true,
	"refactor": true,
	"test":     true,
	"perf":     true,
	"ci":       true,
	"build":    true,
	"revert":   true,
}

// commitHeaderRe matches "type(scope)?!?: subject" — the Conventional
// Commits header grammar. Scope characters are restricted to what this
// repo's package/dir names actually use (alnum, underscore, dot, slash,
// hyphen); "!" marks a breaking change and is accepted but not required.
var commitHeaderRe = regexp.MustCompile(`^([a-z]+)(\([A-Za-z0-9_./-]+\))?(!)?: (.+)$`)

// sortedCommitTypes renders CommitTypes deterministically for error text.
func sortedCommitTypes() []string {
	out := make([]string, 0, len(CommitTypes))
	for t := range CommitTypes {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// firstLine returns s up to (not including) its first newline — the
// Conventional Commits header line. A commit message's body and trailers
// (Signed-off-by:, etc.) live on later lines and are never format-checked
// by this gate; only the header is normative for the type(scope): subject
// grammar.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// ValidateCommitMessage checks msg's header line against the Conventional
// Commits grammar and CommitTypes. It never inspects body/trailer lines,
// so a well-formed multi-line message with a Signed-off-by trailer passes
// exactly like a single-line one.
func ValidateCommitMessage(msg string) error {
	header := strings.TrimRight(firstLine(msg), " \t\r")
	if header == "" {
		return errors.New("empty commit message")
	}
	m := commitHeaderRe.FindStringSubmatch(header)
	if m == nil {
		return fmt.Errorf("header %q does not match conventional-commit format type(scope)?: subject", header)
	}
	typ, subject := m[1], m[4]
	if !CommitTypes[typ] {
		return fmt.Errorf("commit type %q is not a repo-standard type (%s)", typ, strings.Join(sortedCommitTypes(), ", "))
	}
	if strings.TrimSpace(subject) == "" {
		return fmt.Errorf("empty subject after type %q", typ)
	}
	return nil
}

// CommitRef is one commit in a range: its message (header + body +
// trailers) and whether it is a merge commit (more than one parent).
// Merge is derived from real parent-count data (LoadCommitRange), not from
// sniffing the subject text, so a merge with an unusual subject is still
// correctly skipped.
type CommitRef struct {
	SHA     string
	Message string
	Merge   bool
}

// CommitViolation is one commit in a range that failed ValidateCommitMessage.
type CommitViolation struct {
	SHA     string
	Subject string
	Reason  string
}

// ValidateCommitRange validates every non-merge commit in commits and
// returns a CommitViolation per failure, in input order.
func ValidateCommitRange(commits []CommitRef) []CommitViolation {
	var out []CommitViolation
	for _, c := range commits {
		if c.Merge {
			continue
		}
		if err := ValidateCommitMessage(c.Message); err != nil {
			out = append(out, CommitViolation{SHA: c.SHA, Subject: firstLine(c.Message), Reason: err.Error()})
		}
	}
	return out
}

// RunConventionalCommitGate is the gate entry point: nil when every
// non-merge commit in commits passes, otherwise a single aggregated error
// listing every violation (one line each) for the caller to report.
func RunConventionalCommitGate(commits []CommitRef) error {
	violations := ValidateCommitRange(commits)
	if len(violations) == 0 {
		return nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "conventional-commit gate: %d violation(s):\n", len(violations))
	for _, v := range violations {
		fmt.Fprintf(&b, "  - %s: %s\n", v.Subject, v.Reason)
	}
	return errors.New(strings.TrimRight(b.String(), "\n"))
}

// gitLogFieldSep / gitLogRecordSep delimit LoadCommitRange's `git log`
// output: field separator between hash/parents/body, record separator
// between commits. Both are C0 control characters (unit/record
// separator) that cannot occur in a text commit message written through
// a normal editor, so no escaping is needed.
const (
	gitLogFieldSep  = "\x1f"
	gitLogRecordSep = "\x1e"
)

// LoadCommitRange runs real `git log` over revRange (e.g. "base..head", or
// a single ref meaning "everything reachable from it") in root and returns
// each commit's CommitRef, oldest-parsing-order as git emits them. This is
// the real-git-log-output path Art.2 requires — parseGitLogOutput is the
// pure part tested separately with synthetic bytes.
func LoadCommitRange(root, revRange string) ([]CommitRef, error) {
	cmd := exec.Command("git", "-C", root, "log", "--no-color",
		"--pretty=format:%H"+gitLogFieldSep+"%P"+gitLogFieldSep+"%B"+gitLogRecordSep, revRange)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git log %s: %w", revRange, err)
	}
	return parseGitLogOutput(out), nil
}

// parseGitLogOutput is the pure decoder for LoadCommitRange's --pretty
// format. Exported indirectly via LoadCommitRange; kept separate so
// commits_test.go can fuzz-free unit test it against hand-built byte
// slices without invoking git at all.
func parseGitLogOutput(out []byte) []CommitRef {
	var refs []CommitRef
	for _, rec := range strings.Split(string(out), gitLogRecordSep) {
		rec = strings.TrimPrefix(rec, "\n")
		if strings.TrimSpace(rec) == "" {
			continue
		}
		parts := strings.SplitN(rec, gitLogFieldSep, 3)
		if len(parts) != 3 {
			continue
		}
		sha, parents, body := parts[0], strings.Fields(parts[1]), strings.TrimSuffix(parts[2], "\n")
		refs = append(refs, CommitRef{SHA: sha, Message: body, Merge: len(parents) > 1})
	}
	return refs
}
