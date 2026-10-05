// Purpose (this file): Redact, the R-21.200/R-21.162 privacy pass over a
// composed WidgetSnapshot — split out of widget.go under the repo's
// 300-line file cap (compositor_build.go's own established remedy for
// this package).
//
// Inputs: a WidgetSnapshot (widget.go's Compose output) and the resolved
// [widget].show_project_names flag.
// Outputs: a WidgetSnapshot safe to leave the daemon: every project/scope
// label is the stable neutral "Project N" form unless showProjectNames is
// true, and every row field is scrubbed of the five R-21.162 PII pattern
// families regardless of that flag.
//
// PII DETECTION (recorded, not borrowed from an existing helper — none
// exists in this tree; grepping for "PII" outside internal/secrets/tags.go
// returns nothing, and that file's PII tag is a classification label, not
// a detector). The five families are matched by pattern, not by an
// allowlist of known-bad values, so a value this ticket's test fixture
// never anticipated is still caught. A Ref field that matches any pattern,
// or that leaves the widget charset (WidgetRef below), is replaced by a
// short deterministic hash of itself (stable across
// calls with the same input, and structurally incapable of carrying the
// original PII) rather than a partial in-place scrub, which could leave a
// PII fragment behind or break Ref's opaque-stable-id contract. A Label
// field that matches is replaced with the literal "redacted" — Label
// carries no stability contract, only a display one.
//
// SPORT: fleet.capacity.widget (ADD, Redact half, P1-E38-W8-S74-T1).

package capacity

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
)

// piiPatterns is the closed set of five R-21.162 PII pattern families.
// Order matters only for readability; every pattern is checked against
// every candidate string.
var piiPatterns = []*regexp.Regexp{
	// email
	regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`),
	// remote URL (http/https/ssh/git/ftp scheme)
	regexp.MustCompile(`(?i)\b(?:https?|ssh|git|ftp)://\S+`),
	// absolute path, unix (two or more /-separated segments) or windows
	// (a drive letter root)
	regexp.MustCompile(`(?:^|[\s"'` + "`" + `])(?:/[A-Za-z0-9_.\-]+){2,}`),
	regexp.MustCompile(`[A-Za-z]:\\[A-Za-z0-9_.\\\-]+`),
	// hostname-shaped token: two or more dot-separated labels ending in a
	// bare local/TLD-shaped suffix (.local, .lan, or a 2+ letter TLD),
	// checked after email above so an email's domain part is not
	// double-matched as a bare hostname finding of its own.
	regexp.MustCompile(`\b[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?(?:\.[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?){1,}\.(?:local|lan|[A-Za-z]{2,})\b`),
	// username-shaped token: an @handle not already matched as an email
	// (no following domain part).
	regexp.MustCompile(`@[A-Za-z0-9_-]{2,32}\b(?:[^.]|$)`),
}

// containsPII reports whether s matches any of the five PII families.
func containsPII(s string) bool {
	for _, p := range piiPatterns {
		if p.MatchString(s) {
			return true
		}
	}
	return false
}

// widgetRefPattern is the charset and length every unhashed WidgetRef holds:
// 1 to 64 of letters, digits, dot, underscore and hyphen.
var widgetRefPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// WidgetRef returns the opaque, stable ref the widget feed carries for a
// provider name (and for a node id or project ref): the name itself when it
// matches widgetRefPattern and no PII family, else "ref-" plus the first 12
// hex digits of sha256(name). The hash is deterministic and not reversible,
// so a ref never carries an email, host or path into the App Group file or
// a lock-screen snapshot (R-21.162, R-21.200). `cascade provider reauth`
// resolves such a ref back to one provider by computing the same function
// over the registry it opens (P1-WID-08); this is the one copy of the rule.
func WidgetRef(name string) string {
	if widgetRefPattern.MatchString(name) && !containsPII(name) {
		return name
	}
	sum := sha256.Sum256([]byte(name))
	return "ref-" + hex.EncodeToString(sum[:])[:12]
}

// sanitizeLabel returns label unchanged unless it matches a PII pattern,
// in which case it returns the literal "redacted".
func sanitizeLabel(label string) string {
	if containsPII(label) {
		return "redacted"
	}
	return label
}

// neutralProjectLabel returns the R-21.200 stable neutral label for the
// project/scope at position i (0-based) in the caller's already-ordered
// list: "Project 1", "Project 2", ...
func neutralProjectLabel(i int) string {
	return fmt.Sprintf("Project %d", i+1)
}

// Redact applies the R-21.200/R-21.162 privacy pass to snap, returning a
// new WidgetSnapshot — never mutating the caller's copy. When
// showProjectNames is false, every ProjectRow.Label becomes its stable
// neutral "Project N" form (ordered by the caller's existing Projects
// order, which Compose already produces deterministically from a sorted
// ref key); when true, the real label survives (subject to the
// unconditional PII scrub below). Every Ref/Label field on every row —
// WidgetRow, NodePresenceSummary, ProjectRow — is scrubbed of the five
// PII families regardless of showProjectNames: the ticket's "unconditional"
// rule is stricter than the project-name toggle and applies after it.
func Redact(snap WidgetSnapshot, showProjectNames bool) WidgetSnapshot {
	out := snap
	out.Rows = make([]WidgetRow, len(snap.Rows))
	for i, r := range snap.Rows {
		r.Ref = WidgetRef(r.Ref)
		r.Label = sanitizeLabel(r.Label)
		out.Rows[i] = r
	}

	out.Nodes = make([]NodePresenceSummary, len(snap.Nodes))
	for i, n := range snap.Nodes {
		n.ID = WidgetRef(n.ID)
		out.Nodes[i] = n
	}

	out.Projects = make([]ProjectRow, len(snap.Projects))
	for i, p := range snap.Projects {
		p.Ref = WidgetRef(p.Ref)
		if !showProjectNames {
			p.Label = neutralProjectLabel(i)
		}
		p.Label = sanitizeLabel(p.Label)
		out.Projects[i] = p
	}

	return out
}
