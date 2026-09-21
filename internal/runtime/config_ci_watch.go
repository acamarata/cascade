// Purpose: the `[ci.watch]` config key's typed load -- a flat array of
// inline tables, `ci.watch = [{repo="...", branch="...", workflow="..."}]`
// -- behind Load (config.go), per P1-E25-W5-S51-T4's task 1
// (08-INIT-CONFIG-SPEC.md §3 gains this row, R-21.267) and config_ci.go's
// own sibling-file precedent (config.go/config_sections.go stay the ONE
// registration point R-21.267 describes; every section's own parser lives
// in its own file, since Art.10.3's 300-line cap forbids inlining them
// all into config.go).
//
// SHAPE NOTE (recorded, not papered over). Every other array-shaped
// section in this package ([ci.policy.repos.private], retrieval.sources)
// is a flat array of scalars. A watch entry is a RECORD (repo plus two
// optional globs), so `ci.watch` is modeled as a single key holding an
// array of TOML inline tables -- the one shape toml_edit.go's line-based
// SetKeyLine can still rewrite as ONE line (config_ci_watch_write.go). An
// array-of-tables `[[ci.watch]]` header form would need a multi-line,
// multi-block rewrite that editor does not attempt.
//
// Inputs: the decoded generic config tree (env overrides already applied).
// Outputs: a ciWatchSection, or a typed *ConfigError naming the offending
// entry/field.
// Constraints: fail-closed on an invalid "owner/repo" pattern or an
// invalid branch/workflow glob (06 §5.20) -- reusing config_ci.go's own
// validateRepoPattern rather than a second hand-written check, and the
// SAME path.Match semantics config_ci_watch_write.go's write path and
// internal/ci/attention.go's match-time resolver apply, so all three
// agree on what a valid pattern is.
// SPORT: internal.runtime.parseCIWatchSection/ADDED,
//
//	internal.runtime.CIWatchEntry/ADDED (P1-E25-W5-S51-T4).

package runtime

import (
	"fmt"
	"path"
)

// CIWatchEntry is one `[ci.watch]` row: a repo the operator wants CI
// failures routed to attention for, with optional branch/workflow glob
// filters (empty = match every branch/workflow).
type CIWatchEntry struct {
	Repo     string
	Branch   string
	Workflow string
}

// Validate checks one entry in isolation -- the same rule Load applies to
// every parsed entry and config_ci_watch_write.go's WriteCIWatchEntries
// applies before ever touching disk.
func (e CIWatchEntry) Validate() error {
	if err := validateRepoPattern(e.Repo); err != nil {
		return &ConfigError{Field: "ci.watch.repo", Reason: err.Error()}
	}
	if err := validateCIWatchGlob("branch", e.Branch); err != nil {
		return err
	}
	if err := validateCIWatchGlob("workflow", e.Workflow); err != nil {
		return err
	}
	return nil
}

// validateCIWatchGlob checks one optional glob field at LOAD/write time,
// matching validateRepoPattern's own path.Match-based discipline. An
// empty pattern (match-everything) is always valid.
func validateCIWatchGlob(field, pattern string) error {
	if pattern == "" {
		return nil
	}
	if _, err := path.Match(pattern, "probe"); err != nil {
		return &ConfigError{Field: "ci.watch." + field, Reason: "is not a valid glob pattern: " + err.Error()}
	}
	return nil
}

// ciWatchSection is the `[ci.watch]` key's typed view.
type ciWatchSection struct {
	Entries []CIWatchEntry
}

// parseCIWatchSection type-checks tree's ci.watch key: an array of
// tables, each carrying a required "repo" string and optional
// "branch"/"workflow" strings. An absent key parses to zero entries, not
// an error -- matching every other optional section in this package.
func parseCIWatchSection(tree map[string]interface{}) (ciWatchSection, error) {
	ciTree, ok := tree["ci"].(map[string]interface{})
	if !ok {
		return ciWatchSection{}, nil
	}
	raw, present := ciTree["watch"]
	if !present {
		return ciWatchSection{}, nil
	}
	list, ok := raw.([]interface{})
	if !ok {
		return ciWatchSection{}, &ConfigError{Field: "ci.watch", Reason: "must be an array of tables"}
	}
	entries := make([]CIWatchEntry, 0, len(list))
	for i, v := range list {
		entry, err := decodeCIWatchEntry(i, v)
		if err != nil {
			return ciWatchSection{}, err
		}
		if err := entry.Validate(); err != nil {
			return ciWatchSection{}, err
		}
		entries = append(entries, entry)
	}
	return ciWatchSection{Entries: entries}, nil
}

// decodeCIWatchEntry type-checks one ci.watch[i] element, rejecting any
// key other than repo/branch/workflow (the wire shape IS the allow-list,
// matching internal/fleet/supervision/rpc.go's decodeAttnParams
// precedent).
func decodeCIWatchEntry(i int, v interface{}) (CIWatchEntry, error) {
	m, ok := v.(map[string]interface{})
	if !ok {
		return CIWatchEntry{}, &ConfigError{Field: fmt.Sprintf("ci.watch[%d]", i), Reason: "must be a table with a \"repo\" key"}
	}
	for k := range m {
		if k != "repo" && k != "branch" && k != "workflow" {
			return CIWatchEntry{}, &ConfigError{Field: fmt.Sprintf("ci.watch[%d].%s", i, k), Reason: "unrecognised key in a [ci.watch] entry"}
		}
	}
	entry := CIWatchEntry{}
	var err error
	if entry.Repo, err = ciWatchStringField(i, m, "repo", true); err != nil {
		return CIWatchEntry{}, err
	}
	if entry.Branch, err = ciWatchStringField(i, m, "branch", false); err != nil {
		return CIWatchEntry{}, err
	}
	if entry.Workflow, err = ciWatchStringField(i, m, "workflow", false); err != nil {
		return CIWatchEntry{}, err
	}
	return entry, nil
}

// ciWatchStringField reads one string field from a decoded ci.watch[i]
// table, failing closed on a missing required key or a non-string value.
func ciWatchStringField(i int, m map[string]interface{}, key string, required bool) (string, error) {
	raw, present := m[key]
	if !present {
		if required {
			return "", &ConfigError{Field: fmt.Sprintf("ci.watch[%d].%s", i, key), Reason: "is required"}
		}
		return "", nil
	}
	s, ok := raw.(string)
	if !ok {
		return "", &ConfigError{Field: fmt.Sprintf("ci.watch[%d].%s", i, key), Reason: "must be a string"}
	}
	return s, nil
}
