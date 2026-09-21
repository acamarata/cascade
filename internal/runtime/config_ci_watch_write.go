// Purpose: the read-modify-write and idempotent-merge helpers
// `cascade github ci watch add|list|remove` (cmd/cascade/
// github_ci_watch_cmd.go) needs to persist [ci.watch] entries, split from
// config_ci_watch.go purely to keep that file's own 300-line budget for
// the parse/validate side (Art.10.3).
//
// Inputs: a full desired []CIWatchEntry (WriteCIWatchEntries), or the
// current entries plus one add/remove operand (UpsertCIWatchEntry /
// RemoveCIWatchEntry).
// Outputs: WriteCIWatchEntries persists the whole set atomically via the
// SAME structure-preserving edit + Validate + atomic-write pipeline
// ConfigWriter.Set uses (toml_edit.go / config_write_secrets.go), or
// leaves disk untouched on any failure; Upsert/Remove are pure,
// I/O-free slice operations a caller composes with a Load beforehand and
// a Write afterward.
// Constraints: every entry is Validate()'d before any byte is written
// (06 §5.20 fail-closed); add is idempotent on CIWatchEntry.Repo (exact
// string match) per this ticket's AC4 -- a second add for the same repo
// UPDATES the existing entry's Branch/Workflow rather than appending a
// second one; remove of an absent repo is a no-op, never an error.
// SPORT: internal.runtime.WriteCIWatchEntries/ADDED,
//
//	internal.runtime.UpsertCIWatchEntry/ADDED,
//	internal.runtime.RemoveCIWatchEntry/ADDED (P1-E25-W5-S51-T4).

package runtime

import (
	"strconv"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// WriteCIWatchEntries serializes entries (in the given order) as
// `ci.watch`'s inline-table-array literal and persists it to path,
// validating every entry and the whole resulting document before
// touching disk -- disk is left exactly as it was on any failure.
func WriteCIWatchEntries(path string, entries []CIWatchEntry) error {
	for _, e := range entries {
		if err := e.Validate(); err != nil {
			return err
		}
	}
	literal := renderCIWatchLiteral(entries)
	src, err := readOptionalFile(path)
	if err != nil {
		return err
	}
	if err := refuseCIWatchTableArray(src); err != nil {
		return err
	}
	edited, err := SetKeyLine(src, "ci.watch", literal)
	if err != nil {
		return err
	}
	tree, err := decodeForValidate(edited)
	if err != nil {
		return err
	}
	if err := Validate(tree); err != nil {
		return err
	}
	return writeBytesAtomic(path, edited)
}

// refuseCIWatchTableArray fails closed on a hand-written `[[ci.watch]]`
// array-of-TABLES header form. Both TOML shapes decode to the same
// []interface{}, so a LOAD cannot tell them apart -- but this
// line-oriented writer can only rewrite the single-key inline form, and
// SetKeyLine over a header form produces a duplicate-key document. A
// typed, actionable refusal naming the supported form is the honest
// answer: nothing accepted on read is silently left un-round-trippable
// on write (06 §5.20).
func refuseCIWatchTableArray(src []byte) error {
	for _, line := range strings.Split(string(src), "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "[[") {
			continue
		}
		inner := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(trimmed, "[["), "]]"))
		if inner == "ci.watch" {
			return cascade.Wrap(cascade.KindInvalidInput, &ConfigError{
				Field: "ci.watch",
				Reason: "is written as a [[ci.watch]] array of tables, which this writer cannot update; " +
					"rewrite it as the supported single-key inline form, " +
					"e.g. [ci] followed by watch = [{repo = \"owner/repo\", branch = \"main\"}]",
			}, "runtime: config: cannot update a [[ci.watch]] array of tables")
		}
	}
	return nil
}

// renderCIWatchLiteral renders entries as one TOML array-of-inline-tables
// literal, e.g. `[{repo = "a/b", branch = "main"}, {repo = "c/d"}]`. An
// empty branch/workflow is OMITTED from the rendered table (not written
// as `branch = ""`) so a round-tripped file stays minimal.
func renderCIWatchLiteral(entries []CIWatchEntry) string {
	if len(entries) == 0 {
		return "[]"
	}
	parts := make([]string, 0, len(entries))
	for _, e := range entries {
		fields := []string{"repo = " + tomlQuoteString(e.Repo)}
		if e.Branch != "" {
			fields = append(fields, "branch = "+tomlQuoteString(e.Branch))
		}
		if e.Workflow != "" {
			fields = append(fields, "workflow = "+tomlQuoteString(e.Workflow))
		}
		parts = append(parts, "{"+strings.Join(fields, ", ")+"}")
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// tomlQuoteString renders s as a TOML basic string. strconv.Quote's
// escaping (backslash, double-quote, control characters) is a strict
// subset of TOML basic-string escaping for the ASCII glob/repo patterns
// this key ever carries, so it is reused rather than re-implemented.
func tomlQuoteString(s string) string {
	return strconv.Quote(s)
}

// UpsertCIWatchEntry inserts e into entries, or -- when an entry with the
// same Repo (exact string equality) already exists -- replaces it with e.
// Idempotency (AC4): a second add for the same repo updates the existing
// entry rather than appending a duplicate. Returns the new slice (entries
// itself is never mutated in place) and whether an existing entry was
// updated (false means e was newly appended).
func UpsertCIWatchEntry(entries []CIWatchEntry, e CIWatchEntry) ([]CIWatchEntry, bool) {
	for i, existing := range entries {
		if existing.Repo == e.Repo {
			out := append([]CIWatchEntry(nil), entries...)
			out[i] = e
			return out, true
		}
	}
	out := append([]CIWatchEntry(nil), entries...)
	return append(out, e), false
}

// RemoveCIWatchEntry removes the entry whose Repo equals repo (exact
// string equality). Returns the new slice and whether anything was
// removed -- removing an absent repo is a successful no-op (AC4), never
// an error.
func RemoveCIWatchEntry(entries []CIWatchEntry, repo string) ([]CIWatchEntry, bool) {
	out := make([]CIWatchEntry, 0, len(entries))
	removed := false
	for _, existing := range entries {
		if existing.Repo == repo {
			removed = true
			continue
		}
		out = append(out, existing)
	}
	return out, removed
}
